package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func runChat(args []string) error {
	prompt, args, _ := flagValue(args, "-p", "--prompt")
	providerName, args, _ := flagValue(args, "--provider")
	model, args, _ := flagValue(args, "--model")
	cfgPath, args, _ := flagValue(args, "--config")
	if len(args) > 0 {
		return fmt.Errorf("chat: unexpected arguments: %v", args)
	}

	res, err := config.Load(config.LoadOptions{
		ConfigPath:         cfgPath,
		ProviderOverride:   providerName,
		ModelOverride:      model,
		AllowMissingConfig: true,
	})
	if err != nil {
		return err
	}
	if !res.APIKeySet {
		return fmt.Errorf("missing API key: set %s in environment or env file (see mivia doctor)", res.APIKeyEnv)
	}

	comp, err := provider.New(res)
	if err != nil {
		return err
	}
	sess := chat.NewSession(res, comp)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if prompt != "" {
		return oneShot(ctx, sess, prompt)
	}
	return repl(ctx, sess, res)
}

func oneShot(ctx context.Context, sess *chat.Session, prompt string) error {
	fmt.Fprintf(os.Stderr, "mivia chat provider=%s model=%s\n", sess.Completer.Name(), sess.Model)
	_, err := sess.SendUser(ctx, prompt, os.Stdout)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	return nil
}

func repl(ctx context.Context, sess *chat.Session, res *config.Resolved) error {
	fmt.Fprintf(os.Stderr, "mivia chat  provider=%s model=%s\n", res.ProviderName, res.Model)
	fmt.Fprintf(os.Stderr, "Type a message, or 'exit' / Ctrl-D to quit. Ctrl-C cancels in-flight request.\n")
	in := bufio.NewScanner(os.Stdin)
	// Large pastes
	buf := make([]byte, 0, 64*1024)
	in.Buffer(buf, 1024*1024)

	for {
		fmt.Fprint(os.Stderr, "you> ")
		if !in.Scan() {
			fmt.Fprintln(os.Stderr)
			break
		}
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		fmt.Fprint(os.Stderr, "mivia> ")
		_, err := sess.SendUser(ctx, line, os.Stdout)
		fmt.Fprintln(os.Stdout)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("cancelled")
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			// continue REPL on error
			continue
		}
	}
	if err := in.Err(); err != nil {
		return err
	}
	return nil
}
