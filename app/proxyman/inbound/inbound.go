package inbound

//go:generate go run $GOPATH/src/v2ray.com/core/common/errors/errorgen/main.go -pkg inbound -path App,Proxyman,Inbound

import (
	"context"

	"v2ray.com/core"
	"v2ray.com/core/app/proxyman"
	"v2ray.com/core/common"
)

// Manager is to manage all inbound handlers.
type Manager struct {
	handlers       []core.InboundHandler
	taggedHandlers map[string]core.InboundHandler
}

func New(ctx context.Context, config *proxyman.InboundConfig) (*Manager, error) {
	m := &Manager{
		taggedHandlers: make(map[string]core.InboundHandler),
	}
	v := core.FromContext(ctx)
	if v == nil {
		return nil, newError("V is not in context")
	}
	if err := v.RegisterFeature((*core.InboundHandlerManager)(nil), m); err != nil {
		return nil, newError("unable to register InboundHandlerManager").Base(err)
	}
	return m, nil
}

func (m *Manager) AddHandler(ctx context.Context, handler core.InboundHandler) error {
	m.handlers = append(m.handlers, handler)
	tag := handler.Tag()
	if len(tag) > 0 {
		m.taggedHandlers[tag] = handler
	}
	return nil
}

func (m *Manager) GetHandler(ctx context.Context, tag string) (core.InboundHandler, error) {
	handler, found := m.taggedHandlers[tag]
	if !found {
		return nil, newError("handler not found: ", tag)
	}
	return handler, nil
}

func (m *Manager) Start() error {
	for _, handler := range m.handlers {
		if err := handler.Start(); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) Close() {
	for _, handler := range m.handlers {
		handler.Close()
	}
}

func NewHandler(ctx context.Context, config *core.InboundHandlerConfig) (core.InboundHandler, error) {
	rawReceiverSettings, err := config.ReceiverSettings.GetInstance()
	if err != nil {
		return nil, err
	}
	receiverSettings, ok := rawReceiverSettings.(*proxyman.ReceiverConfig)
	if !ok {
		return nil, newError("not a ReceiverConfig").AtError()
	}
	proxySettings, err := config.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}
	tag := config.Tag
	allocStrategy := receiverSettings.AllocationStrategy
	if allocStrategy == nil || allocStrategy.Type == proxyman.AllocationStrategy_Always {
		return NewAlwaysOnInboundHandler(ctx, tag, receiverSettings, proxySettings)
	}

	if allocStrategy.Type == proxyman.AllocationStrategy_Random {
		return NewDynamicInboundHandler(ctx, tag, receiverSettings, proxySettings)
	}
	return nil, newError("unknown allocation strategy: ", receiverSettings.AllocationStrategy.Type).AtError()
}

func init() {
	common.Must(common.RegisterConfig((*proxyman.InboundConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return New(ctx, config.(*proxyman.InboundConfig))
	}))
	common.Must(common.RegisterConfig((*core.InboundHandlerConfig)(nil), func(ctx context.Context, config interface{}) (interface{}, error) {
		return NewHandler(ctx, config.(*core.InboundHandlerConfig))
	}))
}
