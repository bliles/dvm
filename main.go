package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "dvm: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("a command is required")
	}

	command := args[0]
	if command == "help" || command == "--help" || command == "-h" {
		printUsage(stdout)
		return nil
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	dirFlag := flags.String("dir", "", "dvm-managed .NET installation directory")
	preview := flags.Bool("preview", false, "install the latest preview instead of the latest stable release")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}

	dir, err := resolveInstallDir(*dirFlag)
	if err != nil {
		return err
	}

	switch command {
	case "install":
		if flags.NArg() != 1 {
			return errors.New("usage: dvm install [--dir PATH] [--preview] MAJOR")
		}
		major, err := parseMajor(flags.Arg(0))
		if err != nil {
			return err
		}
		return installMajors(ctx, dir, []int{major}, *preview, stdout)
	case "uninstall":
		if flags.NArg() != 1 || *preview {
			return errors.New("usage: dvm uninstall [--dir PATH] MAJOR|SDK_VERSION")
		}
		removed, err := uninstallVersion(dir, flags.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Uninstalled .NET %s (%d version directories removed).\n", flags.Arg(0), removed)
		return nil
	case "update":
		if flags.NArg() != 0 {
			return errors.New("usage: dvm update [--dir PATH] [--preview]")
		}
		majors, err := installedMajors(dir)
		if err != nil {
			return err
		}
		if len(majors) == 0 {
			return fmt.Errorf("no dvm-managed .NET SDKs found in %s; install a version first", dir)
		}
		return installMajors(ctx, dir, majors, *preview, stdout)
	case "list":
		if flags.NArg() != 0 {
			return errors.New("usage: dvm list [--dir PATH]")
		}
		return listInstalled(dir, stdout)
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func resolveInstallDir(flagValue string) (string, error) {
	dir := flagValue
	if dir == "" {
		dir = os.Getenv("DVM_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find user home directory: %w", err)
		}
		dir = filepath.Join(home, ".dotnet")
	}

	dir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("resolve install directory: %w", err)
	}
	if _, err := os.Lstat(dir); err == nil {
		dir, err = filepath.EvalSymlinks(dir)
		if err != nil {
			return "", fmt.Errorf("resolve install directory symlinks: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect install directory %s: %w", dir, err)
	}
	return dir, nil
}

func parseMajor(value string) (int, error) {
	major, err := strconv.Atoi(value)
	if err != nil || major < 1 || strings.TrimSpace(value) != value {
		return 0, fmt.Errorf("invalid .NET major version %q; expected a positive integer such as 10", value)
	}
	return major, nil
}

func listInstalled(dir string, stdout io.Writer) error {
	sdks, err := versionDirectories(filepath.Join(dir, "sdk"))
	if err != nil {
		return err
	}
	if len(sdks) == 0 {
		fmt.Fprintln(stdout, "No dvm-managed .NET SDKs found.")
		return nil
	}

	fmt.Fprintln(stdout, "SDKs:")
	for _, version := range sdks {
		fmt.Fprintf(stdout, "  %s\n", version)
	}
	for _, framework := range []string{"Microsoft.NETCore.App", "Microsoft.AspNetCore.App", "Microsoft.WindowsDesktop.App"} {
		versions, err := versionDirectories(filepath.Join(dir, "shared", framework))
		if err != nil {
			return err
		}
		if len(versions) > 0 {
			fmt.Fprintf(stdout, "%s runtimes:\n", framework)
			for _, version := range versions {
				fmt.Fprintf(stdout, "  %s\n", version)
			}
		}
	}
	return nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage:
  dvm install [--dir PATH] [--preview] MAJOR
  dvm uninstall [--dir PATH] MAJOR|SDK_VERSION
  dvm update [--dir PATH] [--preview]
  dvm list [--dir PATH]

The default installation directory is DVM_DIR or ~/.dotnet.`)
}
