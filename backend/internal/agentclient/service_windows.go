package agentclient

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"github.com/vpsmanager/backend/internal/agentwire"
	"golang.org/x/sys/windows/svc"
)

type windowsService struct {
	run func(context.Context) error
	err error
}

func (s *windowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	status := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	changes <- status
	for {
		select {
		case err := <-done:
			s.err = err
			if err != nil && !errors.Is(err, context.Canceled) {
				return true, 1
			}
			return false, 0
		case request, ok := <-requests:
			if !ok {
				cancel()
				requests = nil
				continue
			}
			switch request.Cmd {
			case svc.Interrogate:
				changes <- status
			case svc.Stop, svc.Shutdown:
				status = svc.Status{State: svc.StopPending, WaitHint: 10000}
				changes <- status
				cancel()
			}
		}
	}
}

func runDaemon(c agentwire.Config, version, name string) error {
	service, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if service {
		if name == "" {
			return errors.New("windows service requires --service-name")
		}
		h := &windowsService{run: func(ctx context.Context) error { return Run(ctx, c, version) }}
		if err = svc.Run(name, h); err != nil {
			return err
		}
		return h.err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return Run(ctx, c, version)
}
