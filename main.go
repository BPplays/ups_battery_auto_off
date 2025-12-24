//go:build windows

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"context"
	// "os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"github.com/hekmon/processpriority"
	"github.com/kardianos/service"
)

type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32 // seconds remaining
	BatteryFullLifeTime uint32
}

var dryRun bool = false

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")

	powrprof              = windows.NewLazySystemDLL("powrprof.dll")
	procSetSuspendState   = powrprof.NewProc("SetSuspendState")
)

type program struct{
    exit chan struct{}
}

func (p *program) Start(s service.Service) error {
    // Create the exit channel
    p.exit = make(chan struct{})

    // Start should not block. Do the actual work async.
    go p.run()
    return nil
}

func (p *program) run() {
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    go func() {
        <-p.exit
        cancel() // Cancel context when exit is signaled
    }()

    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            log.Println("Service is running...")
            mainLoop(ctx)
        case <-p.exit:
            log.Println("Exiting run loop...")
            return
        }
    }
}

func (p *program) Stop(s service.Service) error {
    // Signal the run goroutine to exit
    log.Println("Service is stopping...")
    close(p.exit)
    return nil
}

func getPowerStatus() (*systemPowerStatus, error) {
	var s systemPowerStatus
	r, _, err := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&s)))
	if r == 0 {
		return nil, err
	}
	return &s, nil
}


func hibernate() error {
	log.Println("UPS condition met, hibernating")
	if !dryRun {
		// SetSuspendState(BOOL Hibernate, BOOL ForceCritical, BOOL DisableWakeEvent)
		// Hibernate = TRUE (1), ForceCritical = TRUE (1), DisableWakeEvent = FALSE (0)
		r, _, err := procSetSuspendState.Call(
			uintptr(1), // Hibernate (not sleep)
			uintptr(1), // Force critical suspension (apps can't refuse)
			uintptr(0), // 0 to Allow wake events
		)
		if r == 0 {
			return fmt.Errorf("SetSuspendState failed: %w", err)
		}
	}
	return nil

	// if !dryRun {
	// 	if err := exec.Command("shutdown", "/h").Run(); err != nil {
	// 		return fmt.Errorf("hibernate failed: %w", err)
	// 	}
	// }
}

func setPrio() {
	for range 10 {
		priority := processpriority.Idle

		err := processpriority.Set(os.Getpid(), priority)
		if err == nil {
			break
		} else {
			fmt.Println("error setting prio")
		}
	}
}



func mainLoop(ctx context.Context) {

	const (
		checkInterval      = 5 * time.Second
		maxBatterySeconds  = 30 * time.Second
		criticalRemaining  = 3 * 60 // seconds
	)

	dryRunP := flag.Bool("dryRun", false, "don't hibernate")
	flag.Parse()
	dryRun = *dryRunP

	setPrio()

	var onBatterySince time.Time

	for {
		st := time.Now()

		if ctx.Err() != nil {
			return
		}

		status, err := getPowerStatus()
		if err != nil {
			log.Println("power status error:", err)
			time.Sleep(checkInterval)
			continue
		}

		onBattery := status.ACLineStatus == 0
		remaining := status.BatteryLifeTime


		if onBattery {
			fmt.Println("on battery now")
			fmt.Printf("time remaining: %v\n", remaining)
			fmt.Printf("on battery since: %v\n", time.Since(onBatterySince))

			if onBatterySince.IsZero() {
				onBatterySince = time.Now()
			}


			// Condition 1: battery runtime low
			if remaining != 0xFFFFFFFF && remaining <= criticalRemaining {
				hibernate()
				// return
			}

			// Condition 2: on battery too long
			if time.Since(onBatterySince) >= maxBatterySeconds {
				hibernate()
				// return
			}
		} else {
			onBatterySince = time.Time{}
		}

		time.Sleep(checkInterval - time.Since(st))
	}
}

func main() {
	svcConfig := &service.Config{
		Name:        "ups_battery_auto_off",
		DisplayName: "ups_battery_auto_off",
		Description: "this hibernates the computer of the ups has been on battery for more then 30s or if the ups has less then 3 min of batt left",
		Option: service.KeyValue{
			"OnFailure":              "restart",
		},
	}

	for range 25 {
		prg := &program{}
		s, err := service.New(prg, svcConfig)
		if err != nil {
			log.Println(err)
			continue
		}

		err = s.Run()
		if err != nil {
			log.Println(err)
			continue
		}

		break
	}
}
