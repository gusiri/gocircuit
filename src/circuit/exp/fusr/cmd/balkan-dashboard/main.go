package main

import (
	"flag"
	"fmt"
	"os"
	"circuit/kit/sched/limiter"
	"tumblr/balkan/config"
	"tumblr/balkan/dashboard"
	"tumblr/balkan/proto"
	"tumblr/balkan/x"
	tcp "tumblr/balkan/x/plain"
	//_ "circuit/kit/debug/ctrlc"
	_ "circuit/kit/debug/http/trace"
)

// Command-line flags
var (
	flagConfig  = flag.String("config", "", "System-wide config file name")
	flagLevelDB = flag.String("leveldb", "", "Directory for LevelDB")
	flagCache   = flag.Int("cache", -1, "LevelDB in-memory cache size in MB")
	flagIndex   = flag.Int("index", -1, "Index of this node into the config dashboard array, base-0")
)

// XXX: What if all outstanding forwards block?
const MaxOutstandingRequests = 50

func fatalf(_fmt string, _arg ...interface{}) {
	fmt.Fprintf(os.Stderr, _fmt, _arg...)
	os.Exit(1)
}

func main() {
	flag.Parse()

	if *flagIndex < 0 {
		fatalf("Index of dashboard should be specified")
	}

	config, err := config.Read(*flagConfig)
	if err != nil {
		fatalf("read config (%s)", err)
	}
	t := &worker{}

	// Create transport layer
	here := config.Dashboard[*flagIndex]

	// Created embedded DB server
	dialer := tcp.NewDialer()
	if t.srv, err = dashboard.NewServer(dialer, config.Timeline, *flagLevelDB, *flagCache * 1e6); err != nil {
		panic(err)
	}

	t.fwd = newForwarder(dialer, config.Dashboard, here, t.srv)

	// Accept intra-cluster forward requests in a loop
	t.fwdch = StreamForwardRequests(tcp.NewListener(here.Addr))

	// Accept API requests
	t.apich = StreamAPIRequests(here.HTTP)

	t.schedule()
}

type worker struct {
	srv   *dashboard.DashboardServer
	fwd   *forwarder	// Forwards Query requests to the appropriate dashboard shard
	apich <-chan *request	// Incoming API requests
	fwdch <-chan *request	// ?
}

type request struct {
	Source         string	// "http" or "fwd"
	Query          *proto.XDashboardQuery
	ReturnResponse func([]*proto.Post, error)
}

// Accept forward requests
func StreamForwardRequests(x0 x.Listener) <-chan *request {
	ch := make(chan *request)
	go func() {
		for {
			conn := x0.Accept()
			req, err := conn.Read()
			if err != nil {
				conn.Write(&proto.XError{err.Error()})
				conn.Close()
				continue
			}
			fwdreq, ok := req.(*proto.XDashboardQuery)
			if !ok {
				conn.Write(&proto.XError{"unknown dashboard request"})
				conn.Close()
				continue
			}
			ch <- &request{
				Query: fwdreq, 
				ReturnResponse: func(posts []*proto.Post, err error) {
					if err != nil {
						conn.Write(&proto.XError{err.Error()})
					} else {
						conn.Write(&proto.XDashboardQuerySuccess{Posts: posts})
					}
					conn.Close()
				},
			}
		}
	}()
	return ch
}

func (t *worker) schedule() {
	println("Scheduling")
	lmtr := limiter.New(MaxOutstandingRequests)
	for {
		var job *request
		select {
		case job = <-t.fwdch:
		case job = <-t.apich:
		}

		lmtr.Open()
		go func(job *request) {
			defer lmtr.Close()
			job.ReturnResponse(t.fwd.Forward(job.Query, job.Source == "fwd"))
		}(job)
	}
}
