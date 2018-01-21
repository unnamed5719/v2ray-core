package outbound

import (
	"context"
	"io"

	"v2ray.com/core"
	"v2ray.com/core/app/proxyman"
	"v2ray.com/core/app/proxyman/mux"
	"v2ray.com/core/common/errors"
	"v2ray.com/core/common/net"
	"v2ray.com/core/proxy"
	"v2ray.com/core/transport/internet"
	"v2ray.com/core/transport/ray"
)

type Handler struct {
	config          *core.OutboundHandlerConfig
	senderSettings  *proxyman.SenderConfig
	proxy           proxy.Outbound
	outboundManager core.OutboundHandlerManager
	mux             *mux.ClientManager
}

func NewHandler(ctx context.Context, config *core.OutboundHandlerConfig) (*Handler, error) {
	v := core.FromContext(ctx)
	if v == nil {
		return nil, newError("V is not in context")
	}
	h := &Handler{
		config:          config,
		outboundManager: v.OutboundHandlerManager(),
	}

	if config.SenderSettings != nil {
		senderSettings, err := config.SenderSettings.GetInstance()
		if err != nil {
			return nil, err
		}
		switch s := senderSettings.(type) {
		case *proxyman.SenderConfig:
			h.senderSettings = s
		default:
			return nil, newError("settings is not SenderConfig")
		}
	}

	proxyConfig, err := config.ProxySettings.GetInstance()
	if err != nil {
		return nil, err
	}

	proxyHandler, err := proxy.CreateOutboundHandler(ctx, proxyConfig)
	if err != nil {
		return nil, err
	}

	if h.senderSettings != nil && h.senderSettings.MultiplexSettings != nil && h.senderSettings.MultiplexSettings.Enabled {
		config := h.senderSettings.MultiplexSettings
		if config.Concurrency < 1 || config.Concurrency > 1024 {
			return nil, newError("invalid mux concurrency: ", config.Concurrency).AtWarning()
		}
		h.mux = mux.NewClientManager(proxyHandler, h, config)
	}

	h.proxy = proxyHandler
	return h, nil
}

func (h *Handler) Tag() string {
	return h.config.Tag
}

// Dispatch implements proxy.Outbound.Dispatch.
func (h *Handler) Dispatch(ctx context.Context, outboundRay ray.OutboundRay) {
	if h.mux != nil {
		err := h.mux.Dispatch(ctx, outboundRay)
		if err != nil {
			newError("failed to process outbound traffic").Base(err).WriteToLog()
			outboundRay.OutboundOutput().CloseError()
		}
	} else {
		err := h.proxy.Process(ctx, outboundRay, h)
		// Ensure outbound ray is properly closed.
		if err != nil && errors.Cause(err) != io.EOF {
			newError("failed to process outbound traffic").Base(err).WriteToLog()
			outboundRay.OutboundOutput().CloseError()
		} else {
			outboundRay.OutboundOutput().Close()
		}
		outboundRay.OutboundInput().CloseError()
	}
}

var zeroAddr net.Addr = &net.TCPAddr{IP: []byte{0, 0, 0, 0}, Port: 0}

// Dial implements proxy.Dialer.Dial().
func (h *Handler) Dial(ctx context.Context, dest net.Destination) (internet.Connection, error) {
	if h.senderSettings != nil {
		if h.senderSettings.ProxySettings.HasTag() {
			tag := h.senderSettings.ProxySettings.Tag
			handler := h.outboundManager.GetHandler(tag)
			if handler != nil {
				newError("proxying to ", tag, " for dest ", dest).AtDebug().WriteToLog()
				ctx = proxy.ContextWithTarget(ctx, dest)
				stream := ray.NewRay(ctx)
				go handler.Dispatch(ctx, stream)
				return ray.NewConnection(stream, zeroAddr, zeroAddr), nil
			}

			newError("failed to get outbound handler with tag: ", tag).AtWarning().WriteToLog()
		}

		if h.senderSettings.Via != nil {
			ctx = internet.ContextWithDialerSource(ctx, h.senderSettings.Via.AsAddress())
		}

		if h.senderSettings.StreamSettings != nil {
			ctx = internet.ContextWithStreamSettings(ctx, h.senderSettings.StreamSettings)
		}
	}

	return internet.Dial(ctx, dest)
}
