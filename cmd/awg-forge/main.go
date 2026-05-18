package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
	"github.com/astronaut808/awg-forge/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	svc := app.New(cfg)

	switch args[0] {
	case "init":
		_, err := svc.Init()
		return err
	case "serve":
		if _, err := svc.Init(); err != nil {
			return err
		}
		if err := svc.RenderAll(); err != nil {
			return err
		}
		return server.Serve(cfg, svc)
	case "render":
		return svc.RenderAll()
	case "doctor":
		return doctor.Run(cfg, svc)
	case "client":
		return runClient(svc, args[1:])
	case "tunnel":
		return runTunnel(svc, args[1:])
	default:
		return usage()
	}
}

func runTunnel(svc *app.Service, args []string) error {
	if len(args) == 1 && args[0] == "restart" {
		return svc.RestartTunnel()
	}
	if len(args) >= 2 && args[0] == "create" {
		profileID := args[1]
		name, port, subnet := app.SuggestedTunnelSpec(profileID)
		if len(args) > 2 {
			name = args[2]
		}
		if len(args) > 3 {
			n, err := strconv.Atoi(args[3])
			if err != nil {
				return err
			}
			port = n
		}
		if len(args) > 4 {
			subnet = args[4]
		}
		tunnel, err := svc.CreateTunnel(profileID, name, subnet, port)
		if err != nil {
			return err
		}
		fmt.Println(tunnel.ID)
		return nil
	}
	return errors.New("usage: awg-forge tunnel restart | tunnel create <profile> [name] [port] [subnet]")
}

func runClient(svc *app.Service, args []string) error {
	if len(args) < 1 {
		return usage()
	}
	switch args[0] {
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: awg-forge client add <name> [tunnel-id|name|interface]")
		}
		var (
			client config.Client
			err    error
		)
		if len(args) == 3 {
			client, err = svc.AddClientToTunnel(args[2], args[1])
		} else {
			client, err = svc.AddClient(args[1])
		}
		if err != nil {
			return err
		}
		fmt.Println(client.ID)
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client remove <id>")
		}
		return svc.RemoveClient(args[1])
	case "enable":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client enable <id>")
		}
		return svc.SetClientEnabled(args[1], true)
	case "disable":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client disable <id>")
		}
		return svc.SetClientEnabled(args[1], false)
	case "config":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client config <id>")
		}
		conf, err := svc.ClientConfig(args[1])
		if err != nil {
			return err
		}
		fmt.Print(conf)
		return nil
	default:
		return usage()
	}
}

func usage() error {
	return errors.New("usage: awg-forge init|serve|render|doctor|client|tunnel")
}
