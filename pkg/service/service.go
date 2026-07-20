package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"loopworker/pkg/utils"
)

type ServiceState int

const (
	StateStopped ServiceState = iota
	StateStarting
	StateRunning
	StateStopping
	StateFailed
)

func (s ServiceState) String() string {
	return []string{"stopped", "starting", "running", "stopping", "failed"}[s]
}

type Service struct {
	ID        string
	Name      string
	ExecPath  string
	Args      []string
	WorkDir   string
	State     ServiceState
	PID       int
	StartedAt *time.Time
	StoppedAt *time.Time
	Error     error
	mu        sync.RWMutex
}

type ServiceManager struct {
	services map[string]*Service
	mu       sync.RWMutex
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewServiceManager() *ServiceManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &ServiceManager{
		services: make(map[string]*Service),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (m *ServiceManager) Register(service *Service) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.services[service.ID] = service
}

func (m *ServiceManager) Start(id string) error {
	m.mu.Lock()
	service, exists := m.services[id]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("service %s not found", id)
	}
	service.mu.Lock()
	service.State = StateStarting
	service.mu.Unlock()
	m.mu.Unlock()

	cmd := exec.Command(service.ExecPath, service.Args...)
	cmd.Dir = service.WorkDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}

	if err := cmd.Start(); err != nil {
		service.mu.Lock()
		service.State = StateFailed
		service.Error = err
		service.mu.Unlock()
		return fmt.Errorf("start service %s: %w", id, err)
	}

	now := time.Now()
	service.mu.Lock()
	service.PID = cmd.Process.Pid
	service.StartedAt = &now
	service.State = StateRunning
	service.mu.Unlock()

	utils.GoSafe(context.Background(), func(ctx context.Context) {
		_ = cmd.Wait()
		service.mu.Lock()
		if service.State == StateRunning {
			service.State = StateStopped
			now := time.Now()
			service.StoppedAt = &now
		}
		service.mu.Unlock()
	})

	return nil
}

func (m *ServiceManager) Stop(id string) error {
	m.mu.RLock()
	service, exists := m.services[id]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("service %s not found", id)
	}

	service.mu.Lock()
	if service.State != StateRunning {
		service.mu.Unlock()
		return nil
	}
	service.State = StateStopping
	pid := service.PID
	service.mu.Unlock()

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	if runtime.GOOS == "windows" {
		cmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
		_ = cmd.Run()
	} else {
		_ = process.Signal(syscall.SIGTERM)
		time.Sleep(2 * time.Second)
		_ = process.Signal(syscall.SIGKILL)
	}

	service.mu.Lock()
	now := time.Now()
	service.StoppedAt = &now
	service.State = StateStopped
	service.mu.Unlock()

	return nil
}

func (m *ServiceManager) StopAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.services))
	for id := range m.services {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for i := len(ids) - 1; i >= 0; i-- {
		_ = m.Stop(ids[i])
	}
}

func (m *ServiceManager) GetState(id string) ServiceState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	service, exists := m.services[id]
	if !exists {
		return StateStopped
	}
	return service.State
}

func (m *ServiceManager) List() []*Service {
	m.mu.RLock()
	defer m.mu.RUnlock()

	services := make([]*Service, 0, len(m.services))
	for _, service := range m.services {
		services = append(services, service)
	}
	return services
}

func (m *ServiceManager) WatchSignals() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	utils.GoSafe(context.Background(), func(ctx context.Context) {
		<-sigCh
		m.StopAll()
		m.cancel()
		os.Exit(0)
	})
}

func (m *ServiceManager) Cleanup() {
	m.StopAll()
	m.cancel()
}

func CreatePIDFile(path string) error {
	pid := os.Getpid()
	return os.WriteFile(path, []byte(fmt.Sprintf("%d", pid)), 0644)
}

func ReadPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	_, err = fmt.Sscanf(string(data), "%d", &pid)
	return pid, err
}

func RemovePIDFile(path string) {
	os.Remove(path)
}

func IsProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

type ProcessGuard struct {
	pidFile string
	service *Service
}

func NewProcessGuard(pidFile string) *ProcessGuard {
	return &ProcessGuard{pidFile: pidFile}
}

func (g *ProcessGuard) Acquire() bool {
	if _, err := os.Stat(g.pidFile); err == nil {
		pid, err := ReadPIDFile(g.pidFile)
		if err == nil && IsProcessRunning(pid) {
			return false
		}
	}

	if err := CreatePIDFile(g.pidFile); err != nil {
		return false
	}

	return true
}

func (g *ProcessGuard) Release() {
	RemovePIDFile(g.pidFile)
}

func GetDefaultWorkDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".loopworker")
}

func EnsureDirectories(dirs ...string) error {
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return nil
}
