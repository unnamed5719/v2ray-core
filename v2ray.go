package core

import (
	"context"
	"sync"

	"v2ray.com/core/common"
	"v2ray.com/core/common/uuid"
)

// Server is an instance of V2Ray. At any time, there must be at most one Server instance running.
// Deprecated. Use Instance directly.
type Server interface {
	common.Runnable
}

// Feature is the interface for V2Ray features. All features must implement this interface.
// All existing features have an implementation in app directory. These features can be replaced by third-party ones.
type Feature interface {
	common.Runnable
}

// Instance combines all functionalities in V2Ray.
type Instance struct {
	dnsClient     syncDNSClient
	policyManager syncPolicyManager
	dispatcher    syncDispatcher
	router        syncRouter
	ihm           syncInboundHandlerManager
	ohm           syncOutboundHandlerManager
	clock         syncClock
	cmd           syncCommander

	access   sync.Mutex
	features []Feature
	id       uuid.UUID
	running  bool
}

// New returns a new V2Ray instance based on given configuration.
// The instance is not started at this point.
// To make sure V2Ray instance works properly, the config must contain one Dispatcher, one InboundHandlerManager and one OutboundHandlerManager. Other features are optional.
func New(config *Config) (*Instance, error) {
	var server = &Instance{
		id: uuid.New(),
	}

	if err := config.Transport.Apply(); err != nil {
		return nil, err
	}

	for _, appSettings := range config.App {
		settings, err := appSettings.GetInstance()
		if err != nil {
			return nil, err
		}
		if _, err := server.CreateObject(settings); err != nil {
			return nil, err
		}
	}

	for _, inbound := range config.Inbound {
		rawHandler, err := server.CreateObject(inbound)
		if err != nil {
			return nil, err
		}
		handler, ok := rawHandler.(InboundHandler)
		if !ok {
			return nil, newError("not an InboundHandler")
		}
		if err := server.InboundHandlerManager().AddHandler(context.Background(), handler); err != nil {
			return nil, err
		}
	}

	for _, outbound := range config.Outbound {
		rawHandler, err := server.CreateObject(outbound)
		if err != nil {
			return nil, err
		}
		handler, ok := rawHandler.(OutboundHandler)
		if !ok {
			return nil, newError("not an OutboundHandler")
		}
		if err := server.OutboundHandlerManager().AddHandler(context.Background(), handler); err != nil {
			return nil, err
		}
	}

	return server, nil
}

func (s *Instance) CreateObject(config interface{}) (interface{}, error) {
	ctx := context.WithValue(context.Background(), v2rayKey, s)
	return common.CreateObject(ctx, config)
}

// ID returns an unique ID for this V2Ray instance.
func (s *Instance) ID() uuid.UUID {
	return s.id
}

// Close shutdown the V2Ray instance.
func (s *Instance) Close() error {
	s.access.Lock()
	defer s.access.Unlock()

	s.running = false
	for _, f := range s.features {
		f.Close()
	}

	return nil
}

// Start starts the V2Ray instance, including all registered features. When Start returns error, the state of the instance is unknown.
// A V2Ray instance can be started only once. Upon closing, the instance is not guaranteed to start again.
func (s *Instance) Start() error {
	s.access.Lock()
	defer s.access.Unlock()

	s.running = true
	for _, f := range s.features {
		if err := f.Start(); err != nil {
			return err
		}
	}

	newError("V2Ray started").AtWarning().WriteToLog()

	return nil
}

// RegisterFeature registers the given feature into V2Ray.
// If feature is one of the following types, the corressponding feature in this Instance
// will be replaced: DNSClient, PolicyManager, Router, Dispatcher, InboundHandlerManager, OutboundHandlerManager.
func (s *Instance) RegisterFeature(feature interface{}, instance Feature) error {
	switch feature.(type) {
	case DNSClient, *DNSClient:
		s.dnsClient.Set(instance.(DNSClient))
	case PolicyManager, *PolicyManager:
		s.policyManager.Set(instance.(PolicyManager))
	case Router, *Router:
		s.router.Set(instance.(Router))
	case Dispatcher, *Dispatcher:
		s.dispatcher.Set(instance.(Dispatcher))
	case InboundHandlerManager, *InboundHandlerManager:
		s.ihm.Set(instance.(InboundHandlerManager))
	case OutboundHandlerManager, *OutboundHandlerManager:
		s.ohm.Set(instance.(OutboundHandlerManager))
	case Clock, *Clock:
		s.clock.Set(instance.(Clock))
	case Commander, *Commander:
		s.cmd.Set(instance.(Commander))
	}
	s.access.Lock()
	defer s.access.Unlock()

	s.features = append(s.features, instance)
	if s.running {
		return instance.Start()
	}
	return nil
}

// DNSClient returns the DNSClient used by this Instance. The returned DNSClient is always functional.
func (s *Instance) DNSClient() DNSClient {
	return &(s.dnsClient)
}

// PolicyManager returns the PolicyManager used by this Instance. The returned PolicyManager is always functional.
func (s *Instance) PolicyManager() PolicyManager {
	return &(s.policyManager)
}

// Router returns the Router used by this Instance. The returned Router is always functional.
func (s *Instance) Router() Router {
	return &(s.router)
}

// Dispatcher returns the Dispatcher used by this Instance. If Dispatcher was not registered before, the returned value doesn't work, although it is not nil.
func (s *Instance) Dispatcher() Dispatcher {
	return &(s.dispatcher)
}

// InboundHandlerManager returns the InboundHandlerManager used by this Instance. If InboundHandlerManager was not registered before, the returned value doesn't work.
func (s *Instance) InboundHandlerManager() InboundHandlerManager {
	return &(s.ihm)
}

// OutboundHandlerManager returns the OutboundHandlerManager used by this Instance. If OutboundHandlerManager was not registered before, the returned value doesn't work.
func (s *Instance) OutboundHandlerManager() OutboundHandlerManager {
	return &(s.ohm)
}

// Clock returns the Clock used by this Instance. The returned Clock is always functional.
func (s *Instance) Clock() Clock {
	return &(s.clock)
}

// Commander returns the Commander used by this Instance. The returned Commander is always functional.
func (s *Instance) Commander() Commander {
	return &(s.cmd)
}
