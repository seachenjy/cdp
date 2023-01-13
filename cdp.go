package cdp

// 接管chrome调试模式，多tab执行调试任务
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrorOpenRemoteURL   = errors.New("ErrorOpenRemoteURL 调试地址访问失败")
	ErrorTabWsDisconnect = errors.New("ErrorTabWsDisconnect websocket连接已经断开")
	ErrorTimeout         = errors.New("ErrorTimeout 超时")
)

type RemoteClient struct {
	RemoteUrl  *url.URL //调试地址
	Client     *http.Client
	TabSize    int    //最多允许打开的tab标签数量
	Tabs       []*Tab //tab池
	TabChan    chan *Tab
	TabCallers []TabCaller
	Lock       *sync.Mutex
}

// tab执行器
// 返回tab指针，会回收进tab池
type TabCaller func(tab *Tab)

// tab标签
type Tab struct {
	Description          string          `json:"description"`
	DevtoolsFrontendUrl  string          `json:"devtoolsFrontendUrl"`
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Type                 string          `json:"type"`
	Url                  string          `json:"url"`
	WebSocketDebuggerUrl string          `json:"webSocketDebuggerUrl"`
	WsConn               *websocket.Conn `json:"-"`
	ReqID                int
	Lock                 *sync.Mutex
	ResponseCall         map[int]wsResponseCall
}

type wsResponseCall func(message wsMessage)

type wsMessage struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`

	Method string          `json:"Method"`
	Params json.RawMessage `json:"Params"`
}

func decode(resp *http.Response, v interface{}) error {
	err := json.NewDecoder(resp.Body).Decode(v)
	resp.Body.Close()
	return err
}

func unmarshal(payload []byte) (map[string]interface{}, error) {
	var response map[string]interface{}
	err := json.Unmarshal(payload, &response)
	if err != nil {
		log.Println("unmarshal", string(payload), len(payload), err)
	}
	return response, err
}

func (c *RemoteClient) GetRequest(method, path string) (*http.Request, error) {
	url, err := c.RemoteUrl.Parse(path)
	if err != nil {
		return nil, err
	}
	return http.NewRequest(method, url.String(), nil)
}

func Init(remoteUrl string, maxTabSize int) (*RemoteClient, error) {
	var client RemoteClient
	u, err := url.Parse(remoteUrl)
	if err != nil {
		return nil, err
	}
	http_client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{},
	}
	client.RemoteUrl = u
	client.Client = http_client
	client.TabSize = maxTabSize
	client.Lock = &sync.Mutex{}
	client.TabChan = make(chan *Tab)
	tabs, err := client.TabList("page")
	if err != nil {
		return nil, err
	}
	fmt.Println("tab length:", len(tabs))
	for i := 0; i < maxTabSize-len(tabs); i++ {
		tab, err := client.NewTab("")
		if err != nil {
			return nil, err
		}
		tabs = append(tabs, tab)
	}
	client.Tabs = tabs
	go client.Run()
	return &client, nil
}

// 初始化tab,建立ws侦听轮询
func (tab *Tab) Init() error {
	conn, _, err := websocket.DefaultDialer.Dial(tab.WebSocketDebuggerUrl, nil)
	if err != nil {
		return err
	}
	tab.Lock = &sync.Mutex{}
	tab.WsConn = conn
	tab.ResponseCall = make(map[int]wsResponseCall)
	go tab.WsLoop()
	return nil
}

func (tab *Tab) WsLoop() {
	for {
		var message wsMessage
		err := tab.WsConn.ReadJSON(&message)
		if err != nil {
			continue
		}
		caller, ok := tab.ResponseCall[message.ID]
		if ok {
			caller(message)
		}
	}
}

func (c *RemoteClient) Run() {
	for {
		t := <-c.TabChan
		c.Lock.Lock()
		if len(c.TabCallers) > 0 {
			caller := c.TabCallers[0]
			go func(t *Tab) {
				caller(t)
				c.TabChan <- t
			}(t)
			c.TabCallers = c.TabCallers[1:]
		} else {
			c.Tabs = append(c.Tabs, t)
		}
		c.Lock.Unlock()
	}
}

func (c *RemoteClient) Reset(t *Tab) {
	t.WsConn.Close()
	c.CloseTab(t)
	tab, err := c.NewTab("")
	if err != nil {
		fmt.Println(err)
	}
	c.TabChan <- tab
}

// 添加一个执行任务
func (c *RemoteClient) Do(caller TabCaller) {
	c.Lock.Lock()
	if len(c.Tabs) > 0 {
		tab := c.Tabs[0]
		if tab.WsConn == nil {
			err := tab.Init()
			if err != nil {
				c.TabCallers = append(c.TabCallers, caller)
				go c.Reset(tab)
			}
		}
		go func(tab *Tab) {
			caller(tab)
			c.TabChan <- tab
		}(tab)
		c.Tabs = c.Tabs[1:]
	} else {
		c.TabCallers = append(c.TabCallers, caller)
	}
	c.Lock.Unlock()
}

func (c *RemoteClient) NewTab(url string) (*Tab, error) {
	path := "/json/new"
	if url != "" {
		path += "?" + url
	}
	req, err := c.GetRequest("GET", path)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, ErrorOpenRemoteURL
	}

	var tab Tab
	if err = decode(resp, &tab); err != nil {
		return nil, err
	}

	return &tab, nil
}

func (c *RemoteClient) CloseTab(t *Tab) error {
	path := "/json/close/" + t.ID
	req, err := c.GetRequest("GET", path)
	if err != nil {
		return err
	}
	_, err = c.Client.Do(req)
	if err != nil {
		return ErrorOpenRemoteURL
	}
	return nil
}

func (c *RemoteClient) TabList(filter string) ([]*Tab, error) {
	req, err := c.GetRequest("GET", "/json/list")
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, ErrorOpenRemoteURL
	}

	var tabs []*Tab

	if err = decode(resp, &tabs); err != nil {
		return nil, err
	}

	if filter == "" {
		return tabs, nil
	}

	var filtered []*Tab

	for _, t := range tabs {
		if t.Type == filter {
			filtered = append(filtered, t)
		}
	}

	return filtered, nil
}

func GetWs(t *Tab) (*websocket.Conn, error) {
	if t.WsConn == nil {
		d := &websocket.Dialer{}

		ws, _, err := d.Dial(t.WebSocketDebuggerUrl, nil)
		if err != nil {
			return nil, err
		}
		t.WsConn = ws
		return ws, nil
	}
	return t.WsConn, nil
}

type Params map[string]interface{}

func (p Params) String(k string) string {
	val, _ := p[k].(string)
	return val
}

func (p Params) Int(k string) int {
	val, _ := p[k].(float64)
	return int(val)
}

func (p Params) Bool(k string) bool {
	val, _ := p[k].(bool)
	return val
}

func (p Params) Map(k string) map[string]interface{} {
	val, _ := p[k].(map[string]interface{})
	return val
}

type EvaluateError struct {
	ErrorDetails     map[string]interface{}
	ExceptionDetails map[string]interface{}
}

func (err EvaluateError) Error() string {
	desc := err.ErrorDetails["description"].(string)
	if excp := err.ExceptionDetails; excp != nil {
		if excp["exception"] != nil {
			desc += fmt.Sprintf(" at line %v col %v",
				excp["lineNumber"].(float64), excp["columnNumber"].(float64))
		}
	}

	return desc
}

func (t *Tab) SendRequest(method string, params Params, caller wsResponseCall) error {
	t.Lock.Lock()
	t.ReqID++
	if caller != nil {
		t.ResponseCall[t.ReqID] = caller
	}
	command := Params{
		"id":     t.ReqID,
		"method": method,
		"params": params,
	}
	t.Lock.Unlock()

	return t.WsConn.WriteJSON(command)
}

func (t *Tab) Evalate(expr string, timeout time.Duration) (interface{}, error) {
	params := Params{
		"expression":    expr,
		"returnByValue": true,
	}
	resch := make(chan interface{})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	err := t.SendRequest("Runtime.evaluate", params, func(res wsMessage) {

		result, err := unmarshal(res.Result)

		if err != nil {
			fmt.Println(err)
			cancel()
		}
		if subtype, ok := result["subtype"]; ok && subtype.(string) == "error" {
			fmt.Println(err)
			cancel()
		}
		if res, ok := result["result"]; ok {
			value, ok := res.(map[string]interface{})["value"].(string)
			if ok {
				resch <- value
			} else {
				cancel()
			}
		}
	})
	if err != nil {
		return nil, err
	}

	select {
	case res := <-resch:
		return res, nil
	case <-ctx.Done():
		return nil, ErrorTimeout
	}
}

func (t *Tab) Navigate(url string) error {
	return t.SendRequest("Page.navigate", Params{
		"url": url,
	}, nil)
}
