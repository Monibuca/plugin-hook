package hook // import "m7s.live/plugin/hook/v4"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/log"
	"m7s.live/engine/v4/util"
)

type HookData struct {
	*Stream `json:"stream"`
	Extra   map[string]interface{} `json:"extra"`
	Event   string                 `json:"event"` //事件名称
	Time    int64                  `json:"time"`  //调用时间
}

type HookAddr struct {
	URL    string
	Header map[string]string
	Method string
}

type HookConfig struct {
	KeepAlive   int
	RetryTimes  int
	BaseURL     string
	Header      map[string]string
	URLList     map[string]string
	RequestList map[string]*HookAddr
	Extra       map[string]interface{}
}

var plugin = InstallPlugin(&HookConfig{
	RetryTimes: 3,
})

func (h *HookConfig) OnEvent(event any) {
	switch v := event.(type) {
	case FirstConfig:
		if h.BaseURL != "" {
			for k, u := range h.URLList {
				if !strings.HasSuffix(u, "http") {
					h.URLList[k] = h.BaseURL + u
				}
			}
			for _, u := range h.RequestList {
				if !strings.HasSuffix(u.URL, "http") {
					u.URL = h.BaseURL + u.URL
				}
				if u.Header != nil {
					if h.Header != nil {
						for k, v := range h.Header {
							u.Header[k] = v
						}
					}
				} else {
					u.Header = h.Header
				}
			}
		}
		for k, u := range h.URLList {
			if _, ok := h.RequestList[k]; !ok {
				h.RequestList[k] = &HookAddr{URL: u, Method: "POST", Header: h.Header}
			}
		}
		h.request("startup", nil)
		var heartreq []*HookAddr
		if v, ok := h.RequestList["keepalive"]; ok {
			heartreq = append(heartreq, v)
		}
		if v, ok := h.RequestList["*"]; ok {
			heartreq = append(heartreq, v)
		}
		if h.KeepAlive > 0 && len(heartreq) > 0 {
			go func(addrs ...*HookAddr) {
				for {
					for _, addr := range addrs {
						h.doRequest(addr, &HookData{
							Event: "keepalive",
						})
					}
					time.Sleep(time.Second * time.Duration(h.KeepAlive))
				}
			}(heartreq...)
		}
	case SEpublish:
		h.request("publish", v.Stream)
	case SEclose:
		h.request("streamClose", v.Stream)
	case ISubscriber:
		h.request("subscribe", v.GetIO().Stream)
	}
}
func (h *HookConfig) request(event string, stream *Stream) {
	if req, ok := h.RequestList[event]; ok {
		go h.doRequest(req, &HookData{
			Stream: stream,
			Event:  event,
		})
	}
	if req, ok := h.RequestList["*"]; ok {
		go h.doRequest(req, &HookData{
			Stream: stream,
			Event:  event,
		})
	}
}
func (h *HookConfig) doRequest(req *HookAddr, data *HookData) (err error) {
	data.Time = time.Now().Unix()
	data.Extra = h.Extra
	param, _ := json.Marshal(data)
	var resp *http.Response
	// Execute the request
	return util.Retry(h.RetryTimes, time.Second, func() error {
		if req.Method == "GET" {
			r, _ := http.NewRequest(req.Method, req.URL, nil)
			r.URL.Query().Set("event", data.Event)
			if data.Stream != nil {
				r.URL.Query().Set("stream", data.Stream.Path)
			} else {
			}
			resp, err = http.DefaultClient.Do(r)
		} else {
			resp, err = http.DefaultClient.Post(req.URL, "application/json", bytes.NewBuffer(param))
		}
		if err != nil {
			// Retry
			log.Warnf("post %s error: %s", req.URL, err.Error())
			return err
		}
		defer resp.Body.Close()

		s := resp.StatusCode
		switch {
		case s >= 500:
			// Retry
			return fmt.Errorf("server %s error: %v", req.URL, s)
		case s >= 400:
			// Don't retry, it was client's fault
			return util.RetryStopErr(fmt.Errorf("client %s error: %v", req.URL, s))
		default:
			// Happy
			return nil
		}
	})
}
