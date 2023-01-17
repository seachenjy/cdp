package cdp

// 接管chrome调试模式，多tab执行调试任务
import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
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
				if err != nil {
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

	t.wsConn.WriteJSON(command)

	reply := <-responseChan

	close(responseChan)
	t.lock.Lock()
	delete(t.responses, reqID)
	t.lock.Unlock()

	return reply, nil
}

func (t *Tab) GetResponseBody(req string) ([]byte, error) {
	res, err := t.SendRequest("Network.getResponseBody", Params{
		"requestId": req,
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
