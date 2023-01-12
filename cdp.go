package cdp

// 接管chrome调试模式，多tab执行调试任务
import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type RemoteClient struct {
	RemoteUrl  *url.URL //调试地址
	Client     *http.Client
	TabSize    int //最多允许打开的tab标签数量
	Tabs       []*Tab
	TabChan    chan *Tab
	TabCallers []TabCaller
	Lock       *sync.Mutex
}

type TabCaller func(tab *Tab) *Tab

// 单例
var connected = false

// tab标签
type Tab struct {
	Description          string          `json:"description"`
	DevtoolsFrontendUrl  string          `json:"devtoolsFrontendUrl"`
	ID                   string          `json:"id"`
	Title                string          `json:"title"`
	Type                 string          `json:"string"`
	Url                  string          `json:"url"`
	WebSocketDebuggerUrl string          `json:"webSocketDebuggerUrl"`
	WsConn               *websocket.Conn `json:"-"`
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

// 初始化，单例
func New(remoteUrl string, maxTabSize int) (*RemoteClient, error) {
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
	connected = true
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
	go client.Run()
	return &client, nil
}

func (c *RemoteClient) Run() {
	for {
		t := <-c.TabChan
		c.Lock.Lock()
		if len(c.TabCallers) > 0 {
			caller := c.TabCallers[0]
			go func() {
				c.TabChan <- caller(t)
			}()
			c.TabCallers = c.TabCallers[1:]
		} else {
			c.Tabs = append(c.Tabs, t)
		}
		c.Lock.Unlock()
	}
}

// 添加一个执行任务
func (c *RemoteClient) Do(caller TabCaller) {
	c.Lock.Lock()
	if len(c.Tabs) > 0 {
		tab := c.Tabs[0]
		go func() {
			c.TabChan <- caller(tab)
		}()
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
		return nil, err
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
		return err
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
		return nil, err
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

func getWs(t *Tab) (*websocket.Conn, error) {
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

func (t *Tab) SendRequest(command string, params Params) (map[string]interface{}, error) {

}

func (t *Tab) Evalate(expr string) (interface{}, error) {
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
