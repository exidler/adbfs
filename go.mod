module github.com/exidler/adbfs

go 1.20

require (
	github.com/alecthomas/kingpin/v2 v2.3.2
	github.com/exidler/goadb v0.0.0-20201208042340-620e0e950ed7
	github.com/hanwen/go-fuse/v2 v2.3.0
	github.com/pmylund/go-cache v1.0.0
	github.com/sirupsen/logrus v0.8.7-0.20150819001102-27b713cfd274
	github.com/stretchr/testify v1.8.2
	golang.org/x/net v0.0.0-20150903014153-db8e4de5b2d6
)

require (
	github.com/alecthomas/units v0.0.0-20211218093645-b94a6e3cc137 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/xhit/go-str2duration/v2 v2.1.0 // indirect
	golang.org/x/sys v0.9.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/exidler/goadb => ./../goadb
