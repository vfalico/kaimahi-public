// Package kagentcurrent is the bounded public client for the selected native
// kagent API. The embedded descriptors are generated from canonical upstream
// schemas; no runtime internals, Guardd transport, or policy schema is imported.
package kagentcurrent

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed public.fds
var descriptors []byte
var descriptorFiles = sync.OnceValues(func() (*protoregistry.Files, error) {
	var set descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(descriptors, &set); err != nil {
		return nil, err
	}
	return protodesc.NewFiles(&set)
})

type Client struct {
	conn    *grpc.ClientConn
	address string
}

func Dial(address string) (*Client, error) {
	host, port, e := net.SplitHostPort(address)
	number, x := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if e != nil || x != nil || ip == nil || !ip.IsLoopback() || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return nil, fmt.Errorf("current API requires literal loopback")
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4<<20), grpc.MaxCallSendMsgSize(4<<20)))
	if err != nil {
		return nil, err
	}
	return &Client{conn, address}, nil
}
func (c *Client) Close() error { return c.conn.Close() }
func (c *Client) Call(ctx context.Context, service, method string, input any) (map[string]any, error) {
	files, err := descriptorFiles()
	if err != nil {
		return nil, err
	}
	d, err := files.FindDescriptorByName(protoreflect.FullName("kagent.api.v1." + service))
	if err != nil {
		return nil, err
	}
	svc, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("invalid public service")
	}
	m := svc.Methods().ByName(protoreflect.Name(method))
	if m == nil {
		return nil, fmt.Errorf("unknown public method")
	}
	request, response := dynamicpb.NewMessage(m.Input()), dynamicpb.NewMessage(m.Output())
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if err = protojson.Unmarshal(raw, request); err != nil {
		return nil, fmt.Errorf("invalid public request: %w", err)
	}
	// Explicit development principal only. This is never remote authentication.
	ctx = metadata.AppendToOutgoingContext(ctx, "x-user-id", "kmx-current-development")
	if err = c.conn.Invoke(ctx, "/kagent.api.v1."+service+"/"+method, request, response); err != nil {
		return nil, err
	}
	raw, err = protojson.Marshal(response)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	err = json.Unmarshal(raw, &value)
	return value, err
}
func (c *Client) Send(ctx context.Context, namespace, name string, input any) ([]byte, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://"+c.address+"/agents/"+namespace+"/"+name, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Id", "kmx-current-development")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, MaxResponseHeaderBytes: 65536, ResponseHeaderTimeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 || response.StatusCode != 200 {
		return nil, fmt.Errorf("current kagent response unavailable")
	}
	return raw, nil
}

// Create replays the exact request across an uncertain transport response. A
// bounded recovery context also works after caller cancellation; it cannot
// create a different operation. The caller still owns deletion of a recovered
// Session, even when the original case has expired.
func (c *Client) Create(ctx context.Context, input any) (map[string]any, error) {
	response, err := c.Call(ctx, "SessionService", "CreateSession", input)
	if err == nil {
		return response, nil
	}
	recovery, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	retry, retryErr := c.Call(recovery, "SessionService", "CreateSession", input)
	if retryErr != nil {
		return nil, errors.Join(err, retryErr)
	}
	return retry, nil
}
