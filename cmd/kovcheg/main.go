// Команда kovcheg — узел и кошелёк сети «Ковчег».
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/api"
	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
	"github.com/makeluaurights/kovchegchain/internal/wallet"
)

const (
	defaultNode    = "http://127.0.0.1:7070"
	defaultHTTP    = "127.0.0.1:7070"
	defaultGenesis = "genesis.json"
	defaultChainID = "kovcheg-local-1"
	defaultMinFee  = "0.001"
	creatorSupply  = "1000000000" // 1 000 000 000 KVG создателю
)

const usage = `kovcheg — узел и кошелёк сети «Ковчег» (KVG)

Команды:
  kovcheg wallet new                 создать кошелёк (~/.kovcheg/wallet.json)
  kovcheg wallet address             показать адрес кошелька
  kovcheg genesis init               создать genesis.json: 1 000 000 000 KVG на адрес кошелька
  kovcheg node                       запустить узел (HTTP на 127.0.0.1:7070)
  kovcheg balance [адрес]            баланс адреса (по умолчанию — своего кошелька)
  kovcheg send <кому> <сумма>        перевести KVG, сумма в KVG: 10, 0.5

Флаги команды: kovcheg <команда> -h
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "wallet":
		err = cmdWallet(args)
	case "genesis":
		err = cmdGenesis(args)
	case "node":
		err = cmdNode(args)
	case "balance":
		err = cmdBalance(args)
	case "send":
		err = cmdSend(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "неизвестная команда %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("ошибка: %v", err)
	}
}

func kovchegDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kovcheg"
	}
	return filepath.Join(home, ".kovcheg")
}

func defaultWalletPath() string { return filepath.Join(kovchegDir(), "wallet.json") }

// parseFlags разбирает флаги в любом месте строки: "send kvg.. 10 -fee 0.01" тоже работает.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func cmdWallet(args []string) error {
	if len(args) == 0 {
		return errors.New("используйте: kovcheg wallet new | kovcheg wallet address")
	}
	fs := flag.NewFlagSet("wallet "+args[0], flag.ExitOnError)
	path := fs.String("wallet", defaultWalletPath(), "файл кошелька")
	if _, err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "new":
		w, err := wallet.New()
		if err != nil {
			return err
		}
		if err := w.Save(*path); err != nil {
			return err
		}
		fmt.Printf("Кошелёк создан: %s\nАдрес: %s\n", *path, w.Address)
		fmt.Println("Сделайте копию файла: без него доступ к монетам не восстановить.")
		return nil
	case "address":
		w, err := wallet.Load(*path)
		if err != nil {
			return err
		}
		fmt.Println(w.Address)
		return nil
	default:
		return fmt.Errorf("неизвестная подкоманда wallet %q", args[0])
	}
}

func cmdGenesis(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errors.New("используйте: kovcheg genesis init")
	}
	fs := flag.NewFlagSet("genesis init", flag.ExitOnError)
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк создателя")
	addrFlag := fs.String("address", "", "адрес создателя (вместо кошелька)")
	out := fs.String("out", defaultGenesis, "куда записать genesis.json")
	chainID := fs.String("chain-id", defaultChainID, "идентификатор сети")
	minFee := fs.String("min-fee", defaultMinFee, "минимальная комиссия, KVG")
	if _, err := parseFlags(fs, args[1:]); err != nil {
		return err
	}

	var addr core.Address
	if *addrFlag != "" {
		a, err := core.ParseAddress(*addrFlag)
		if err != nil {
			return err
		}
		addr = a
	} else {
		w, err := wallet.Load(*walletPath)
		if err != nil {
			return fmt.Errorf("%w (сначала: kovcheg wallet new)", err)
		}
		addr = w.Address
	}

	g := core.Genesis{
		ChainID:   *chainID,
		Timestamp: time.Now().Unix(),
		MinFee:    *minFee,
		Alloc:     []core.GenesisAlloc{{Address: addr, Amount: creatorSupply}},
	}
	if err := g.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s уже существует — не перезаписываю", *out)
		}
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Printf("Записан %s: %s KVG на адрес %s\n", *out, creatorSupply, addr)
	return nil
}

func cmdNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ExitOnError)
	dataDir := fs.String("data", filepath.Join(kovchegDir(), "data"), "папка базы узла")
	genesisPath := fs.String("genesis", defaultGenesis, "файл генезиса")
	httpAddr := fs.String("http", defaultHTTP, "адрес HTTP API")
	producerFlag := fs.String("producer", "", "адрес для комиссий (по умолчанию — адрес кошелька или первый адрес генезиса)")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк узла (для адреса комиссий)")
	interval := fs.Duration("interval", 5*time.Second, "интервал сборки блоков")
	emptyBlocks := fs.Bool("empty-blocks", false, "собирать блоки и без транзакций")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}

	g, err := core.LoadGenesis(*genesisPath)
	if err != nil {
		return fmt.Errorf("%w (создать: kovcheg genesis init)", err)
	}

	var producer core.Address
	switch {
	case *producerFlag != "":
		if producer, err = core.ParseAddress(*producerFlag); err != nil {
			return fmt.Errorf("-producer: %w", err)
		}
	default:
		if w, werr := wallet.Load(*walletPath); werr == nil {
			producer = w.Address
		} else {
			producer = g.Alloc[0].Address
		}
	}

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		return err
	}
	store, err := storage.Open(filepath.Join(*dataDir, "chain.db"))
	if err != nil {
		return err
	}
	defer store.Close()

	cfg := chain.DefaultConfig(producer)
	cfg.EmptyBlocks = *emptyBlocks
	c, err := chain.New(store, g, cfg)
	if err != nil {
		return err
	}

	if host, _, err := net.SplitHostPort(*httpAddr); err == nil {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			log.Printf("ВНИМАНИЕ: API слушает %s — он доступен не только с этого устройства", *httpAddr)
		}
	}
	ln, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: api.NewHandler(c), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	head := c.Head()
	log.Printf("Ковчег: сеть %s, блок #%d %s", c.Params().ChainID, head.Height, head.Hash)
	log.Printf("комиссии -> %s, блок каждые %s", producer, *interval)
	log.Printf("API: http://%s", ln.Addr())

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP: %v", err)
			stop()
		}
	}()
	c.Run(ctx, *interval, log.Default())

	log.Printf("остановка узла...")
	shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shCtx)
}

func cmdBalance(args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк (если адрес не указан)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	var addr core.Address
	switch len(pos) {
	case 0:
		w, err := wallet.Load(*walletPath)
		if err != nil {
			return fmt.Errorf("укажите адрес: kovcheg balance <адрес> (%v)", err)
		}
		addr = w.Address
	case 1:
		if addr, err = core.ParseAddress(pos[0]); err != nil {
			return err
		}
	default:
		return errors.New("используйте: kovcheg balance <адрес>")
	}

	b, err := api.NewClient(*node).Balance(addr)
	if err != nil {
		return err
	}
	fmt.Printf("Адрес:   %s\nБаланс:  %s KVG (%d нано-KVG)\nNonce:   %d\n", b.Address, b.BalanceKVG, b.Balance, b.Nonce)
	if b.PendingNonce != b.Nonce {
		fmt.Printf("В мемпуле: %d транзакц. ожидают блока\n", b.PendingNonce-b.Nonce)
	}
	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк отправителя")
	feeFlag := fs.String("fee", "", "комиссия в KVG (по умолчанию — минимальная сети)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return errors.New("используйте: kovcheg send <кому> <сумма в KVG>")
	}
	to, err := core.ParseAddress(pos[0])
	if err != nil {
		return fmt.Errorf("получатель: %w", err)
	}
	amount, err := core.ParseAmount(pos[1])
	if err != nil {
		return err
	}
	w, err := wallet.Load(*walletPath)
	if err != nil {
		return err
	}

	cl := api.NewClient(*node)
	info, err := cl.Info()
	if err != nil {
		return err
	}
	fee := info.MinFee
	if *feeFlag != "" {
		if fee, err = core.ParseAmount(*feeFlag); err != nil {
			return fmt.Errorf("-fee: %w", err)
		}
	}
	bal, err := cl.Balance(w.Address)
	if err != nil {
		return err
	}

	tx := core.Tx{
		ChainID: info.ChainID,
		To:      to,
		Amount:  amount,
		Fee:     fee,
		Nonce:   bal.PendingNonce,
	}
	tx.Sign(w.Private)
	h, err := cl.SubmitTx(&tx)
	if err != nil {
		return err
	}
	fmt.Printf("Отправлено %s KVG -> %s (комиссия %s KVG, nonce %d)\nХэш: %s\n",
		core.FormatAmount(amount), to, core.FormatAmount(fee), tx.Nonce, h)
	fmt.Printf("Статус: curl '%s/tx?hash=%s'\n", *node, h)
	return nil
}
