package cdp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var (
	ErrorOpenRemoteURL   = errors.New("ErrorOpenRemoteURL 调试地址访问失败")
	ErrorTabWsDisconnect = errors.New("ErrorTabWsDisconnect websocket连接已经断开")
	ErrorTimeout         = errors.New("ErrorTimeout 超时")
)

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
