/*
Another FUSE filesystem that can mount any device visible to your adb server.
Uses github.com/exidler/goadb to interface with the server directly
instead of calling out to the adb client program.

See package adbfs for the filesystem implementation.
*/
package main

import (
	"errors"
	"fmt"
	adb "github.com/exidler/goadb"
	"github.com/hanwen/go-fuse/v2/fuse/nodefs"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	fs "github.com/exidler/adbfs"
	"github.com/exidler/adbfs/internal/cli"
	. "github.com/exidler/adbfs/internal/util"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/fuse/pathfs"
)

const StartTimeout = 5 * time.Second

var (
	config cli.AdbfsConfig

	server *fuse.Server

	mounted AtomicBool

	// Prevents trying to unmount the server multiple times.
	unmounted AtomicBool
)

func init() {
	cli.RegisterAdbfsFlags(&config)
}

func main() {
	cli.Initialize("adbfs", &config.BaseConfig)

	if config.Mountpoint == "" {
		cli.Log.Fatalln("Mountpoint must be specified. Run with -h.")
	}
	absoluteMountpoint, err := filepath.Abs(config.Mountpoint)
	if err != nil {
		cli.Log.Fatal(err)
	}
	if err = checkValidMountpoint(absoluteMountpoint); err != nil {
		cli.Log.Fatal(err)
	}

	cache := initializeCache(config.CacheTtl)
	adbServer, err := adb.NewWithConfig(config.ServerConfig())
	if err != nil {
		cli.Log.Fatal(err)
	}

	config.DeviceSerial = resolveSerial(adbServer, config.DeviceSerial)
	if config.DeviceSerial == "" {
		cli.Log.Fatalln("Device serial must be specified. Run with -h.")
	}
	cli.Log.Infoln("Using device:", config.DeviceSerial)

	fs := initializeFileSystem(adbServer, absoluteMountpoint, cache)
	go watchForDeviceDisconnected(adbServer, config.DeviceSerial)

	server, _, err = nodefs.MountRoot(absoluteMountpoint, fs.Root(), nil)
	if err != nil {
		cli.Log.Fatal(err)
	}

	serverDone, err := startServer(StartTimeout)
	if err != nil {
		cli.Log.Fatal(err)
	}
	cli.Log.Printf("mounted %s on %s", config.DeviceSerial, absoluteMountpoint)
	mounted.CompareAndSwap(false, true)
	defer unmountServer()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, os.Kill)

	for {
		select {
		case signal := <-signals:
			cli.Log.Println("got signal", signal)
			switch signal {
			case os.Kill, os.Interrupt:
				cli.Log.Println("exiting...")
				return
			}

		case <-serverDone:
			cli.Log.Debugln("server done channel closed.")
			return
		}
	}
}

func resolveSerial(adbServer *adb.Adb, serial string) string {
	devices, err := adbServer.ListDevices()
	if err != nil {
		cli.Log.Fatal(err)
	}

	for _, dev := range devices {
		var ds string
		if dev.IsUsb() {
			ds = "usb:"
		} else {
			ds = "tcp:"
		}
		ds = ds + dev.Serial
		cli.Log.Info("device ", ds, ": ", dev.DeviceInfo)

		if strings.Index(ds, serial) >= 0 {
			return dev.Serial
		}
	}

	return ""
}

func initializeCache(ttl time.Duration) fs.DirEntryCache {
	cli.Log.Infoln("stat cache ttl:", ttl)
	return fs.NewDirEntryCache(ttl)
}

func initializeFileSystem(server *adb.Adb, mountpoint string, cache fs.DirEntryCache) *pathfs.PathNodeFs {
	clientFactory := fs.NewCachingDeviceClientFactory(cache,
		fs.NewGoadbDeviceClientFactory(server, config.DeviceSerial, handleDeviceDisconnected))

	var fsImpl pathfs.FileSystem
	fsImpl, err := fs.NewAdbFileSystem(fs.Config{
		DeviceSerial:       config.DeviceSerial,
		Mountpoint:         mountpoint,
		ClientFactory:      clientFactory,
		ConnectionPoolSize: config.ConnectionPoolSize,
		DeviceRoot:         config.DeviceRoot,
		ReadOnly:           config.ReadOnly,
	})
	if err != nil {
		cli.Log.Fatal(err)
	}

	return pathfs.NewPathNodeFs(fsImpl, nil)
}

func watchForDeviceDisconnected(server *adb.Adb, serial string) {
	watcher := server.NewDeviceWatcher()
	defer watcher.Shutdown()

	for {
		select {
		case event, ok := <-watcher.C():
			if !ok {
				// Channel closed.
				break
			}

			if event.Serial == serial && event.WentOffline() {
				cli.Log.Infoln("device disconnected:", serial)
				handleDeviceDisconnected()
			}
		}
	}

	if err := watcher.Err(); err != nil {
		cli.Log.Warn("DeviceWatcher disconnected with error:", err)
	}
}

func startServer(startTimeout time.Duration) (<-chan struct{}, error) {
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		server.Serve()
		cli.Log.Println("server finished.")
		return
	}()

	// Wait for OS to finish initializing the mount.
	// If server.Serve() fails (e.g. mountpoint doesn't exist), WaitMount() won't
	// ever return. Running it in a separate goroutine allows us to detect that case.
	serverReady := make(chan struct{})
	go func() {
		defer close(serverReady)
		server.WaitMount()
	}()

	select {
	case <-serverReady:
		cli.Log.Println("server ready.")
		return serverDone, nil
	case <-serverDone:
		return nil, errors.New("unknown error")
	case <-time.After(startTimeout):
		return nil, errors.New(fmt.Sprint("server failed to start after", startTimeout))
	}
}

func unmountServer() {
	if server == nil {
		panic("attempted to unmount server before creating it")
	}
	if !mounted.Value() {
		panic("attempted to unmount server before mounting it")
	}

	if unmounted.CompareAndSwap(false, true) {
		cli.Log.Println("unmounting...")
		server.Unmount()
		cli.Log.Println("unmounted.")
	}
}

// handleDeviceDisconnected is called either when the DeviceWatcher or the adb.DeviceClient detect
// a device is disconnected.
func handleDeviceDisconnected() {
	if !mounted.Value() || unmounted.Value() {
		// May be called before mounting if device watcher detects disconnection.
		return
	}

	cli.Log.Infoln("device disconnected, unmounting...")
	unmountServer()
}

func checkValidMountpoint(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return errors.New(fmt.Sprint("path is not a directory:", path))
	}

	return nil
}
