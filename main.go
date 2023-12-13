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
	Extra   map[string]any `json:"extra"`
	Event   string         `json:"event"` //事件名称
	Time    int64          `json:"time"`  //调用时间
}

type HookAddr struct {
	URL    string            `desc:"上报远程地址"`
	Header map[string]string `desc:"附加 HTTP 头"`
	Method string            `desc:"HTTP请求方法，默认为POST"`
}

type HookConfig struct {
	KeepAlive   time.Duration     `desc:"保活间隔，0则不发送保活事件"`
	RetryTimes  int               `default:"3" desc:"重试次数"`
	BaseURL     string            `desc:"URL前缀，如果URL不是http开头，则会自动加上前缀"`
	Header      map[string]string `desc:"附加 HTTP 头" key:"名称" value:"值"`
	RequestList map[string]any    `desc:"请求列表是需要单独配置 Method 和 header 时用到的，key为事件名称（*代表全部），value为请求信息,可以是字符串也可以是键值对（可以包含 url、header、method"`
	Extra       map[string]any    `desc:"附加的额外参数"`
	requestList map[string]*HookAddr
}

var HookPlugin = InstallPlugin(&HookConfig{})

func (h *HookConfig) OnEvent(event any) {
	switch v := event.(type) {
	case FirstConfig:
		h.requestList = make(map[string]*HookAddr)
		for k, v := range h.RequestList {
			switch u := v.(type) {
			case string:
				if !strings.HasSuffix(u, "http") && h.BaseURL != "" {
					u = h.BaseURL + u
				}
				h.requestList[k] = &HookAddr{URL: u, Method: "POST", Header: h.Header}
			case map[string]string:
				var addr = HookAddr{
					Method: u["method"],
					Header: h.Header,
					URL:    u["url"],
				}
				if !strings.HasSuffix(addr.URL, "http") && h.BaseURL != "" {
					addr.URL = h.BaseURL + addr.URL
				}
				h.requestList[k] = &addr
			case map[string]any:
				var addr HookAddr
				for k, v := range u {
					switch k {
					case "url":
						addr.URL = v.(string)
						if !strings.HasSuffix(addr.URL, "http") && h.BaseURL != "" {
							addr.URL = h.BaseURL + addr.URL
						}
					case "method":
						addr.Method = v.(string)
					case "header":
						addr.Header = v.(map[string]string)
					}
				}
				if addr.Header == nil {
					addr.Header = h.Header
				} else {
					for k, v := range h.Header {
						addr.Header[k] = v
					}
				}
				h.requestList[k] = &addr
			}
		}

		h.request(&HookData{Event: "startup"})
		var heartreq []*HookAddr
		if v, ok := h.requestList["keepalive"]; ok {
			heartreq = append(heartreq, v)
		}
		if v, ok := h.requestList["*"]; ok {
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
					time.Sleep(h.KeepAlive)
				}
			}(heartreq...)
		}
	case SEpublish:
		h.request(&HookData{Event: "publish", Stream: v.Target})
	case SEclose:
		h.request(&HookData{Event: "streamClose", Stream: v.Target})
	case ISubscriber:
		h.request(&HookData{Event: "subscribe", Stream: v.GetSubscriber().Stream, Extra: map[string]any{"subscriber": v}})
	case UnsubscribeEvent:
		h.request(&HookData{Event: "unsubscribe", Stream: v.Target.GetSubscriber().Stream, Extra: map[string]any{"subscriber": v.Target}})
	}
}
func (h *HookConfig) request(hookData *HookData) {
	if req, ok := h.requestList[hookData.Event]; ok {
		go h.doRequest(req, hookData)
	}
	if req, ok := h.requestList["*"]; ok {
		go h.doRequest(req, hookData)
	}
}
func (h *HookConfig) doRequest(req *HookAddr, data *HookData) (err error) {
	data.Time = time.Now().Unix()
	if data.Extra == nil {
		data.Extra = h.Extra
	} else {
		for k, v := range h.Extra {
			data.Extra[k] = v
		}
	}
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
