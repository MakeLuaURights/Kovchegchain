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
	"strings"
	"syscall"
	"time"

	"github.com/makeluaurights/kovchegchain/internal/api"
	"github.com/makeluaurights/kovchegchain/internal/chain"
	"github.com/makeluaurights/kovchegchain/internal/core"
	"github.com/makeluaurights/kovchegchain/internal/storage"
	"github.com/makeluaurights/kovchegchain/internal/wallet"
)

// Version — версия программы.
const Version = "0.2.0"

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
  kovcheg history [адрес]            история переводов адреса
  kovcheg tx <хэш>                   статус транзакции
  kovcheg block [номер|хэш]          блок (по умолчанию последний)
  kovcheg status                     состояние сети
  kovcheg version                    версия программы

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
	case "history":
		err = cmdHistory(args)
	case "tx":
		err = cmdTx(args)
	case "block":
		err = cmdBlock(args)
	case "status":
		err = cmdStatus(args)
	case "version", "-v", "--version":
		fmt.Println("kovcheg", Version)
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
	cors := fs.String("cors", "", "разрешённые Origin для браузеров через запятую; * — любой")
	rate := fs.Float64("rate", 20, "запросов в секунду с одного IP; 0 — без ограничения")
	readOnly := fs.Bool("readonly", false, "только чтение: не принимать транзакции")
	trustProxy := fs.Bool("trust-proxy", false, "узел за прокси/туннелем: брать IP клиента из X-Forwarded-For")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	if *interval < time.Second {
		return errors.New("-interval не меньше 1s")
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
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("адрес %s занят: возможно, узел уже запущен (другой адрес — флаг -http)", *httpAddr)
		}
		return err
	}
	opt := api.DefaultOptions()
	opt.Version = Version
	opt.BlockInterval = *interval
	opt.ReadOnly = *readOnly
	opt.RateLimit = *rate
	opt.RateBurst = max(1, int(2**rate))
	opt.TrustProxy = *trustProxy
	for _, o := range strings.Split(*cors, ",") {
		if o = strings.TrimSpace(o); o != "" {
			opt.CORSOrigins = append(opt.CORSOrigins, o)
		}
	}
	srv := &http.Server{
		Handler:           api.NewHandler(c, opt),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second, // поток событий и ?wait=true продлевают его сами
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	head := c.Head()
	log.Printf("Ковчег: сеть %s, блок #%d %s", c.Params().ChainID, head.Height, head.Hash)
	log.Printf("комиссии -> %s, блок каждые %s", producer, *interval)
	log.Printf("API: http://%s (версия %s)", ln.Addr(), Version)
	if *readOnly {
		log.Printf("режим только чтения: транзакции не принимаются")
	}

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

// walletOrArg — адрес из аргумента или, если аргумента нет, из кошелька.
func walletOrArg(pos []string, walletPath, cmd string) (core.Address, error) {
	switch len(pos) {
	case 0:
		w, err := wallet.Load(walletPath)
		if err != nil {
			return "", fmt.Errorf("укажите адрес: kovcheg %s <адрес> (%v)", cmd, err)
		}
		return w.Address, nil
	case 1:
		return core.ParseAddress(pos[0])
	default:
		return "", fmt.Errorf("используйте: kovcheg %s [адрес]", cmd)
	}
}

func cmdBalance(args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк (если адрес не указан)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	addr, err := walletOrArg(pos, *walletPath, "balance")
	if err != nil {
		return err
	}
	a, err := api.NewClient(*node).Account(addr)
	if err != nil {
		return err
	}
	fmt.Printf("Адрес:   %s\nБаланс:  %s KVG (%d нано-KVG)\nNonce:   %d\n", a.Address, a.BalanceKVG, a.Balance, a.Nonce)
	if a.PendingTxs > 0 {
		fmt.Printf("В мемпуле: %d транзакц., баланс после них: %s KVG\n", a.PendingTxs, a.PendingBalanceKVG)
	}
	return nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк отправителя")
	feeFlag := fs.String("fee", "", "комиссия в KVG (по умолчанию — минимальная сети)")
	wait := fs.Bool("wait", false, "дождаться попадания в блок")
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
	st, err := cl.Status()
	if err != nil {
		return err
	}
	fee := st.MinFee
	if *feeFlag != "" {
		if fee, err = core.ParseAmount(*feeFlag); err != nil {
			return fmt.Errorf("-fee: %w", err)
		}
	}
	acc, err := cl.Account(w.Address)
	if err != nil {
		return err
	}

	tx := core.Tx{
		ChainID: st.ChainID,
		To:      to,
		Amount:  amount,
		Fee:     fee,
		Nonce:   acc.PendingNonce,
	}
	tx.Sign(w.Private)
	if *wait {
		fmt.Println("Отправляю и жду блок...")
	}
	res, err := cl.SubmitTx(&tx, *wait, 60*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("Отправлено %s KVG -> %s (комиссия %s KVG, nonce %d)\nХэш: %s\n",
		core.FormatAmount(amount), to, core.FormatAmount(fee), tx.Nonce, res.Hash)
	if res.Tx != nil && res.Tx.Block != nil {
		fmt.Printf("Подтверждено в блоке #%d\n", res.Tx.Block.Height)
	} else {
		fmt.Printf("Ждёт блока. Проверить: kovcheg tx %s\n", res.Hash)
	}
	return nil
}

func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	walletPath := fs.String("wallet", defaultWalletPath(), "кошелёк (если адрес не указан)")
	limit := fs.Int("limit", 20, "сколько записей показать (до 100)")
	before := fs.String("before", "", "показать записи старше этой (значение из «Дальше:»)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	addr, err := walletOrArg(pos, *walletPath, "history")
	if err != nil {
		return err
	}
	list, err := api.NewClient(*node).AccountTxs(addr, *before, *limit)
	if err != nil {
		return err
	}
	if len(list.Pending) == 0 && len(list.Txs) == 0 {
		fmt.Println("Транзакций нет.")
		return nil
	}
	for _, t := range list.Pending {
		printHistoryLine("ожидает", t, addr)
	}
	for _, t := range list.Txs {
		printHistoryLine(fmt.Sprintf("блок %d", t.Block.Height), t, addr)
	}
	if list.Next != "" {
		fmt.Printf("Дальше: kovcheg history %s -before %s\n", addr, list.Next)
	}
	return nil
}

func printHistoryLine(where string, t api.TxView, self core.Address) {
	sign, other := "-", t.To
	switch t.Direction {
	case "in":
		sign, other = "+", t.From
	case "self":
		sign = "="
	}
	when := ""
	if t.Block != nil {
		when = time.Unix(t.Block.Time, 0).Format("2006-01-02 15:04:05") + "  "
	}
	fmt.Printf("%s%-10s %s%s KVG  %s  %s\n", when, where, sign, t.AmountKVG, other, t.Hash.String()[:16])
}

func cmdTx(args []string) error {
	fs := flag.NewFlagSet("tx", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("используйте: kovcheg tx <хэш>")
	}
	h, err := core.ParseHash(strings.ToLower(pos[0]))
	if err != nil {
		return err
	}
	t, err := api.NewClient(*node).Tx(h)
	if err != nil {
		return err
	}
	fmt.Printf("Хэш:       %s\nСтатус:    %s\nОт:        %s\nКому:      %s\nСумма:     %s KVG\nКомиссия:  %s KVG\nNonce:     %d\n",
		t.Hash, t.Status, t.From, t.To, t.AmountKVG, t.FeeKVG, t.Nonce)
	if t.Block != nil {
		fmt.Printf("Блок:      #%d (%s), подтверждений: %d\n",
			t.Block.Height, time.Unix(t.Block.Time, 0).Format("2006-01-02 15:04:05"), t.Confirmations)
	}
	return nil
}

func cmdBlock(args []string) error {
	fs := flag.NewFlagSet("block", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	id := "latest"
	switch len(pos) {
	case 0:
	case 1:
		id = pos[0]
	default:
		return errors.New("используйте: kovcheg block [номер|хэш]")
	}
	b, err := api.NewClient(*node).Block(id)
	if err != nil {
		return err
	}
	producer := string(b.Producer)
	if producer == "" {
		producer = "— (генезис)"
	}
	fmt.Printf("Блок #%d\nХэш:            %s\nПредыдущий:     %s\nВремя:          %s\nПроизводитель:  %s\nТранзакций:     %d\nПодтверждений:  %d\n",
		b.Height, b.Hash, b.PrevHash, time.Unix(b.Time, 0).Format("2006-01-02 15:04:05"), producer, b.TxCount, b.Confirmations)
	for _, t := range b.Txs {
		fmt.Printf("  %s  %s -> %s  %s KVG\n", t.Hash.String()[:16], t.From, t.To, t.AmountKVG)
	}
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	node := fs.String("node", defaultNode, "адрес узла")
	if _, err := parseFlags(fs, args); err != nil {
		return err
	}
	st, err := api.NewClient(*node).Status()
	if err != nil {
		return err
	}
	fmt.Printf("Сеть:            %s (узел %s)\nВысота:          %d\nПоследний блок:  %s (%s)\nТранзакций:      %d\nАккаунтов:       %d\nМонет всего:     %s KVG\nМин. комиссия:   %s KVG\nВ мемпуле:       %d\n",
		st.ChainID, st.Version, st.Height, st.HeadHash, time.Unix(st.HeadTime, 0).Format("2006-01-02 15:04:05"),
		st.TxCount, st.AccountCount, st.SupplyKVG, st.MinFeeKVG, st.MempoolTxs)
	if st.ReadOnly {
		fmt.Println("Узел только для чтения.")
	}
	return nil
}
