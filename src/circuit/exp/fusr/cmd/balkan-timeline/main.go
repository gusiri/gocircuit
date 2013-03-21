package main

import (
	"flag"
	"fmt"
	"os"
	"circuit/kit/sched/limiter"
	"sync"
	"tumblr/balkan/config"
	"tumblr/balkan/proto"
	"tumblr/balkan/timeline"
	"tumblr/balkan/x"
	tcp "tumblr/balkan/x/plain"
	// _ "circuit/kit/debug/ctrlc"
	_ "circuit/kit/debug/http/trace"
)

// Command-line flags
var (
	flagConfig  = flag.String("config", "", "System-wide config file name")
	flagLevelDB = flag.String("leveldb", "", "Directory for LevelDB")
	flagCache   = flag.Int("cache", -1, "LevelDB in-memory cache size in MB")
	flagIndex   = flag.Int("index", -1, "Index of this node into the config timeline array, base-0")
	flagFire    = flag.Bool("nofire", false, "Do not read from the Firehose")
	flagFilter  = flag.String("filter", "", "File containing non-excluded timeline IDs")
)

const MaxOutstandingRequests = 300

func fatalf(_fmt string, _arg ...interface{}) {
	fmt.Fprintf(os.Stderr, _fmt, _arg...)
	os.Exit(1)
}

func main() {
	flag.Parse()

	if *flagIndex < 0 {
		fatalf("Index of timeline should be specified")
	}

	config, err := config.Read(*flagConfig)
	if err != nil {
		fatalf("read config (%s)", err)
	}
	t := &worker{}

	var filter Filter
	if *flagFilter != "" {
		if filter, err = ParseFilter(*flagFilter); err != nil {
			fatalf("Error parsing filter (%s)\n", err)
		}
		if len(filter) == 0 {
			fatalf("Empty filter")
		}
	}

	// Created embedded DB server
	if t.srv, err = timeline.NewServer(*flagLevelDB, *flagCache * 1e6); err != nil {
		panic(err)
	}

	here := config.Timeline[*flagIndex]
	t.fwd = newForwarder(tcp.NewDialer(), config.Timeline, here, t.srv, filter)

	t.xCreate, t.xQuery = StreamX(tcp.NewListener(here.Addr))
	t.hCreate, t.hQuery = StreamHTTP(here.HTTP)
	if !*flagFire {
		t.fCreate = StreamFirehose(config.Firehose)
	}

	t.schedule()
}

type worker struct {
	srv      *timeline.TimelineServer
	fwd      *forwarder

	fCreate <-chan *createRequest
	hCreate <-chan *createRequest
	xCreate <-chan *createRequest
	xQuery  <-chan *queryRequest
	hQuery  <-chan *queryRequest
}

type createRequest struct {
	Forwarded      bool
	Post           *proto.XCreatePost
	ReturnResponse func(error)
}

type queryRequest struct {
	Query          *proto.XTimelineQuery
	ReturnResponse func([]int64, error)
}

func StreamX(x0 x.Listener) (<-chan *createRequest, <-chan *queryRequest) {
	cch, qch := make(chan *createRequest), make(chan *queryRequest)
	go func() {
		for {
			conn := x0.Accept()
			req, err := conn.Read()
			if err != nil {
				conn.Write(&proto.XError{err.Error()})
				conn.Close()
				continue
			}
			switch q := req.(type) {
			case *proto.XCreatePost:
				cch <- &createRequest{
					Forwarded:      true,
					Post:           q,
					ReturnResponse: func(err error) {
						if err != nil {
							conn.Write(&proto.XError{err.Error()})
							conn.Close()
							return
						}
						conn.Write(&proto.XSuccess{})
						conn.Close()
					},
				}
			case *proto.XTimelineQuery:
				qch <- &queryRequest{
					Query:          q,
					ReturnResponse: func(posts []int64, err error) {
						if err != nil {
							conn.Write(&proto.XError{err.Error()})
							conn.Close()
							return
						}
						conn.Write(&proto.XTimelineQuerySuccess{Posts: posts})
						conn.Close()
					},
				}
			default:
				panic(fmt.Sprintf("unknown request to timeline: %#v", req))
				conn.Write(&proto.XError{"unknown request"})
				conn.Close()
			}
		}
	}()
	return cch, qch
}

func (t *worker) schedule() {
	println("Scheduling")

	var (
		lk   sync.Mutex
		nxqb int64
		nxqe int64
	)

	lmtr := limiter.New(MaxOutstandingRequests)
	for {
		var job interface{}
		select {
		case job = <-t.fCreate:
		case job = <-t.hCreate:
			//println("+ Processing HTTP-originated XCreatePost")
		case job = <-t.xCreate:
		case job = <-t.xQuery:
			//println("+ Processing X-originated XTimelineQuery")
		case job = <-t.hQuery:
			//println("+ Processing HTTP-originated XTimelineQuery")
		}
		lk.Lock()
		nxqb++
		lk.Unlock()

		lmtr.Open()
		go func(job interface{}) {
			defer lmtr.Close()
			switch q := job.(type) {
			case *createRequest:
				q.ReturnResponse(t.fwd.Forward(q.Post, q.Forwarded))
			case *queryRequest:
				q.ReturnResponse(t.srv.Query(q.Query))
			default:
				panic("naah")
			}
			lk.Lock()
			nxqe++
			if nxqb % 1000 == 0 {
				println("+ Finished", nxqe, "/", nxqb)
			}
			lk.Unlock()
		}(job)
	}
}
