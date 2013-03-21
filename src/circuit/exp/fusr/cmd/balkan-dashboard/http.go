package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
	"tumblr/balkan/proto"
)

// Example dashboard HTTP API curls:
//	curl "localhost:5081/dash?d=5&p=55&l=10"	// d=DashboardID, p=BeforePostID, l=Limit

// Accept API requests
func StreamAPIRequests(port int) <-chan *request {
	ch := make(chan *request)
	go func() {
		//mux := http.NewServeMux()
		http.HandleFunc("/dash", func(w http.ResponseWriter, req *http.Request) { handleQuery(ch, w, req) })
		s := &http.Server{
			Addr:           ":" + strconv.Itoa(port),
			//Handler:        mux,
			ReadTimeout:    time.Second,
			WriteTimeout:   10*time.Second,
			MaxHeaderBytes: 1e4,
		}
		panic(s.ListenAndServe())
	}()
	return ch
}

func handleQuery(ch chan<- *request, w http.ResponseWriter, req *http.Request) {
	var err error
	if err = req.ParseForm(); err != nil {
		http.Error(w, "post form not parsing correctly", 400)
		return
	}
	q := &proto.XDashboardQuery{}

	q.DashboardID, err = strconv.ParseInt(req.Form.Get("DashID"), 10, 64)
	if err != nil {
		http.Error(w, "dashboard id missing or fails to parse as an integer", 400)
		return
	}
	q.BeforePostID, err = strconv.ParseInt(req.Form.Get("UpperPostID"), 10, 64)
	if err != nil {
		http.Error(w, "pivot post id missing or fails to parse as an integer", 400)
		return
	}
	q.Limit, err = strconv.Atoi(req.Form.Get("Limit"))
	if err != nil {
		http.Error(w, "limit missing or fails to parse as an integer", 400)
		return
	}
	if q.Limit > 100 {
		http.Error(w, "limit exceeds 100", 400)
		return
	}

	// Read followed timelines, if given
	var follows []string
	println(fmt.Sprintf("FF=%#v", req.Form.Get("Followed")))
	if err = json.Unmarshal([]byte(req.Form.Get("Followed")), &follows); err != nil {
		println(fmt.Sprintf("Error parsing follows array (%s)", err))
		q.Follows = nil
	} else {
		q.Follows = make([]int64, len(follows))
		for i, s := range follows {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				http.Error(w, "followed ID cannot parse", 400)
				return
			}
			q.Follows[i] = id
		}
	}

	println(fmt.Sprintf("XDashQuery DashID=%d UpperPostID=%d Limit=%d Follows=%#v", 
		q.DashboardID, q.BeforePostID, q.Limit, q.Follows))

	done := make(chan struct{})
	ch <- &request{
		Query: q,
		ReturnResponse: func(posts []*proto.Post, err error) {
			defer close(done)
			if err != nil {
				http.Error(w, "internal error: " + err.Error(), 500)
				return
			}
			raw, err := json.Marshal(posts)
			if err != nil {
				http.Error(w, "encoding error: " + err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write(raw)
		},
	}
	<-done
}
