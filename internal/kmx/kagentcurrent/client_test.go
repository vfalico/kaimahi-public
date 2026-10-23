package kagentcurrent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

type stubServer interface{}
type captureServer struct {
	sync.Mutex
	calls int
	first []byte
}

func TestExactCreateLostReplyRecovery(t *testing.T) {
	files, err := descriptorFiles()
	if err != nil {
		t.Fatal(err)
	}
	d, err := files.FindDescriptorByName("kagent.api.v1.SessionService")
	if err != nil {
		t.Fatal(err)
	}
	method := d.(protoreflect.ServiceDescriptor).Methods().ByName("CreateSession")
	capture := &captureServer{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "kagent.api.v1.SessionService", HandlerType: (*stubServer)(nil), Methods: []grpc.MethodDesc{{MethodName: "CreateSession", Handler: func(_ any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		request := dynamicpb.NewMessage(method.Input())
		if err := decode(request); err != nil {
			return nil, err
		}
		raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(request)
		capture.Lock()
		defer capture.Unlock()
		capture.calls++
		if capture.calls == 1 {
			capture.first = raw
			return nil, status.Error(codes.Unavailable, "injected lost response after durable create")
		}
		if string(raw) != string(capture.first) {
			return nil, status.Error(codes.InvalidArgument, "changed retry")
		}
		response := dynamicpb.NewMessage(method.Output())
		if err := protojson.Unmarshal([]byte(`{"session":{"id":"same-original","contextId":"same-context","state":"RUNTIME_STATE_READY"}}`), response); err != nil {
			return nil, err
		}
		return response, nil
	}}}}, capture)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { server.Stop(); <-done }()
	client, err := Dial(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Create(t.Context(), map[string]any{"agent": map[string]any{"namespace": "test", "name": "native"}, "requestId": "same-operation", "executionRequirement": "b3BhcXVl"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(response)
	if string(raw) != `{"session":{"contextId":"same-context","id":"same-original","state":"RUNTIME_STATE_READY"}}` {
		t.Fatal(string(raw))
	}
	capture.Lock()
	defer capture.Unlock()
	if capture.calls != 2 {
		t.Fatal(capture.calls)
	}
}
func TestClientRefusesNonLiteralLoopback(t *testing.T) {
	for _, address := range []string{"localhost:1234", "10.0.0.1:1234", "127.0.0.1:0", "127.0.0.1:01234", "[::]:1234", "invalid"} {
		if client, err := Dial(address); err == nil {
			client.Close()
			t.Fatal("accepted", address)
		}
	}
}
func TestPublicWireRefusesUnknownRequestField(t *testing.T) {
	client, err := Dial("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Call(t.Context(), "SessionService", "CreateSession", map[string]any{"notCanonical": true}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := client.Call(t.Context(), "SessionService", "Unknown", nil); err == nil {
		t.Fatal("unknown method accepted")
	}
}
func TestHTTPNoRedirectOrEnvironmentProxy(t *testing.T) {
	arrivals := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { arrivals++; w.Write([]byte("unexpected")) }))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("environment proxy used") }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer endpoint.Close()
	client, err := Dial(endpoint.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.Send(t.Context(), "test", "native", map[string]any{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if arrivals != 0 {
		t.Fatal("followed redirect")
	}
}
