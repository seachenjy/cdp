package cdp

import (
	"encoding/base64"
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

// tab执行器
// 返回tab指针，会回收进tab池
type TabCaller func(tab *Tab)
type Params map[string]interface{}

// tab标签
type Tab struct {
	Description          string          `json:"description"`
	DevtoolsFrontendUrl  string          `json:"devtoolsFrontendUrl"`
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Type                 string          `json:"type"`
	Url                  string          `json:"url"`
	WebSocketDebuggerUrl string          `json:"webSocketDebuggerUrl"`
	wsConn               *websocket.Conn `json:"-"`
	reqID                int
	lock                 *sync.Mutex
	responses            map[int]chan json.RawMessage
	callbacks            map[string]EventCallback
}
type EventCallback func(params Params)

// ws消息体
type wsMessage struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`

	Method string          `json:"Method"`
	Params json.RawMessage `json:"Params"`
}

type RemoteClient struct {
	RemoteUrl   *url.URL //调试地址
	Client      *http.Client
	TabSize     int    //最多允许打开的tab标签数量
	Tabs        []*Tab //tab池
	TabChan     chan *Tab
	TabCallChan chan TabCaller
	TabCallers  []TabCaller
	lock        *sync.Mutex
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
	client.lock = &sync.Mutex{}
	client.TabChan = make(chan *Tab, maxTabSize)
	client.TabCallChan = make(chan TabCaller)
	tabs, err := client.TabList("page")
	if err != nil {
		return nil, err
	}
	for i := 0; i < maxTabSize-len(tabs); i++ {
		tab, err := client.NewTab("")
		if err != nil {
			return nil, err
		}
		tabs = append(tabs, tab)
	}
	client.Tabs = tabs
	fmt.Println("tab length:", len(tabs))
	go client.Run()
	return &client, nil
}

func (c *RemoteClient) Run() {
	for {
		select {
		case t := <-c.TabChan:
			c.lock.Lock()
			c.Tabs = append(c.Tabs, t)
			c.lock.Unlock()
		case caller := <-c.TabCallChan:
			if len(c.Tabs) > 0 {
				c.lock.Lock()
				tab := c.Tabs[0]
				c.Tabs = c.Tabs[1:]
				c.lock.Unlock()

				if tab.wsConn == nil {
					err := tab.Init()
					if err != nil {
						c.Do(caller)
						go c.Reset(tab)
						continue
					}
				}
				caller(tab)
				// fmt.Printf("done callers:%d tabs:%d id:%s \r\n", len(c.TabCallers), len(c.Tabs), tab.ID)
				c.TabChan <- tab
			} else {
				c.lock.Lock()
				c.TabCallers = append(c.TabCallers, caller)
				c.lock.Unlock()
			}
		default:
			if len(c.TabCallers) > 0 {
				c.lock.Lock()
				caller := c.TabCallers[0]
				c.TabCallers = c.TabCallers[1:]
				c.Do(caller)
				c.lock.Unlock()
			}
		}

	}
}

func (c *RemoteClient) Reset(t *Tab) {
	t.wsConn.Close()
	c.CloseTab(t)
	tab, err := c.NewTab("")
	if err != nil {
		fmt.Println(err)
	}
	c.TabChan <- tab
}

// 添加一个执行任务
func (c *RemoteClient) Do(caller TabCaller) {
	c.TabCallChan <- caller
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

// 初始化tab,建立ws侦听轮询
func (tab *Tab) Init() error {
	conn, _, err := websocket.DefaultDialer.Dial(tab.WebSocketDebuggerUrl, nil)
	if err != nil {
		return err
	}
	tab.lock = &sync.Mutex{}
	tab.wsConn = conn
	tab.responses = make(map[int]chan json.RawMessage)
	tab.callbacks = make(map[string]EventCallback)
	go tab.WsLoop()
	return nil
}

func (tab *Tab) WsLoop() {
	for {
		var message wsMessage
		err := tab.wsConn.ReadJSON(&message)
		if err != nil {
			continue
		} else if message.Method != "" {
			callback, ok := tab.callbacks[message.Method]
			if ok {
				params, err := unmarshal(message.Params)
				if err == nil {
					callback(params)
				}
			}
		} else {
			ch, ok := tab.responses[message.ID]
			if ok {
				ch <- message.Result
			}
		}
	}
}

func GetWs(t *Tab) (*websocket.Conn, error) {
	if t.wsConn == nil {
		d := &websocket.Dialer{}

		ws, _, err := d.Dial(t.WebSocketDebuggerUrl, nil)
		if err != nil {
			return nil, err
		}
		t.wsConn = ws
		return ws, nil
	}
	return t.wsConn, nil
}

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

func (t *Tab) Evaluate(expr string) (interface{}, error) {
	params := Params{
		"expression":    expr,
		"returnByValue": true,
	}

	res, err := t.SendRequest("Runtime.evaluate", params)
	if err != nil {
		return nil, err
	}

	if res == nil {
		return nil, nil
	}

	result := res["result"].(map[string]interface{})
	if subtype, ok := result["subtype"]; ok && subtype.(string) == "error" {
		// this is actually an error
		exception := res["exceptionDetails"].(map[string]interface{})
		return nil, EvaluateError{ErrorDetails: result, ExceptionDetails: exception}
	}

	return result["value"], nil
}

func (t *Tab) Navigate(url string) error {
	_, err := t.SendRequest("Page.navigate", Params{
		"url": url,
	})
	return err
}

func (t *Tab) NetworkEvents(enable bool) error {
	method := "Network"
	if enable {
		method += ".enable"
	} else {
		method += ".disable"
	}
	_, err := t.SendRequest(method, nil)
	return err
}

func (t *Tab) CallbackEvent(method string, cb EventCallback) {
	t.lock.Lock()
	t.callbacks[method] = cb
	t.lock.Unlock()
}

func (t *Tab) ClearCallbacks() {
	t.lock.Lock()
	t.callbacks = nil
	t.lock.Unlock()
}

// SendRequest sends a request and returns the reply as a a map.
func (t *Tab) SendRequest(method string, params Params) (map[string]interface{}, error) {
	rawReply, err := t.sendRawReplyRequest(method, params)
	if err != nil || rawReply == nil {
		return nil, err
	}
	return unmarshal(rawReply)
}

// sendRawReplyRequest sends a request and returns the reply bytes.
func (t *Tab) sendRawReplyRequest(method string, params Params) ([]byte, error) {

	t.lock.Lock()

	responseChan := make(chan json.RawMessage, 1)
	reqID := t.reqID
	t.responses[reqID] = responseChan
	t.reqID++
	t.lock.Unlock()

	command := Params{
		"id":     reqID,
		"method": method,
		"params": params,
	}
	err := t.wsConn.WriteJSON(command)
	if err != nil {
		return nil, err
	}

	reply := <-responseChan
	close(responseChan)
	t.lock.Lock()
	delete(t.responses, reqID)
	t.lock.Unlock()

	return reply, nil
}

func (t *Tab) GetResponseBody(requestId string) ([]byte, error) {
	res, err := t.SendRequest("Network.getResponseBody", Params{
		"requestId": requestId,
	})
	if err != nil {
		return nil, err
	}

	body := res["body"]
	if body == nil {
		return nil, nil
	}

	if b, ok := res["base64Encoded"]; ok && b.(bool) {
		return base64.StdEncoding.DecodeString(body.(string))
	} else {
		return []byte(body.(string)), nil
	}
}

// wait for page loaded before timeout
func (t *Tab) WaitPageComplete(timeout time.Duration) bool {
	ready := make(chan bool)
	go func(c chan bool) {
		for {
			time.Sleep(1 * time.Second)
			status, err := t.Evaluate("document.readyState")
			if err == nil && status.(string) == "complete" {
				c <- true
			}
		}

	}(ready)
	select {
	case <-time.After(timeout):
		return false
	case r := <-ready:
		return r
	}
}
