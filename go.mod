module example.com/alert-bot

go 1.25.1

require (
	//github.com/bybit-exchange/bybit.go.api v0.0.0-20250727214011-c9347d6804d6 // Удалено, так как используем нативные HTTP запросы
	github.com/go-telegram-bot-api/telegram-bot-api/v5 v5.5.1
	github.com/joho/godotenv v1.5.1
	github.com/sirupsen/logrus v1.9.3
	modernc.org/sqlite v1.18.2 // Используем pure Go SQLite драйвер
//github.com/mattn/go-sqlite3 v1.14.22 // Удалено, так как modernc.org/sqlite не требует GCC
)

require (
	github.com/wcharczuk/go-chart/v2 v2.1.2
	gonum.org/v1/plot v0.16.0
)

require (
	codeberg.org/go-fonts/liberation v0.5.0 // indirect
	codeberg.org/go-latex/latex v0.1.0 // indirect
	codeberg.org/go-pdf/fpdf v0.10.0 // indirect
	git.sr.ht/~sbinet/gg v0.6.0 // indirect
	github.com/ajstarks/svgo v0.0.0-20211024235047-1546f124cd8b // indirect
	github.com/campoy/embedmd v1.0.0 // indirect
	github.com/golang/freetype v0.0.0-20170609003504-e2365dfdc4a0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/kballard/go-shellquote v0.0.0-20180428030007-95032a82bc51 // indirect
	github.com/mattn/go-isatty v0.0.16 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20200410134404-eec4a21b6bb0 // indirect
	golang.org/x/image v0.25.0 // indirect
	golang.org/x/mod v0.17.0 // indirect
	golang.org/x/sync v0.12.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	golang.org/x/tools v0.21.1-0.20240508182429-e35e4ccd0d2d // indirect
	lukechampine.com/uint128 v1.1.1 // indirect
	modernc.org/cc/v3 v3.37.0 // indirect
	modernc.org/ccgo/v3 v3.16.9 // indirect
	modernc.org/libc v1.18.0 // indirect
	modernc.org/mathutil v1.5.0 // indirect
	modernc.org/memory v1.3.0 // indirect
	modernc.org/opt v0.1.1 // indirect
	modernc.org/strutil v1.1.3 // indirect
	modernc.org/token v1.0.1 // indirect
)
