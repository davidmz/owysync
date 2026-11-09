# OwySync

[Русский](#owysync-по-русски)

One-way, low-latency directory sync from **Windows** to a **Linux or FreeBSD server over SSH**. It is built for "edit locally, run on a dev server" workflows with large source trees: 10k–200k files and frequent small edits. It replaces setups like dklab RealSync without Cygwin, Perl or rsync.

- A single `owysync.exe`, nothing to install on the server: it uses SSH, SFTP and standard Unix tools.
- Every start runs a full resync; with 100k unchanged files that takes a few seconds. After that, live mode sends each saved file within about a second.
- Mass changes (`git checkout`, first upload) automatically go through tar archives at full link speed.
- Safe by default: it never syncs into a directory without its marker file, never deletes remote sockets, and never follows remote symlinks.

## Quick start

```bat
cd C:\work\myproject
owysync init
```

Review `.owysync\ignore`. The first sync deletes everything on the server that is not in the local directory and not ignored. Then preview and run:

```bat
owysync sync --once --dry-run
owysync sync
```

## Documentation

- [User guide](docs/user-guide.md)

## Building

```bat
go build -ldflags "-X main.version=0.1.0" -o owysync.exe ./cmd/owysync
```

## License

[MIT](LICENSE)

---

## OwySync по-русски

Односторонняя синхронизация каталога с **Windows** на **сервер Linux или FreeBSD по SSH** с низкой задержкой. Сделана для сценария «правлю локально, запускаю на dev-сервере» с большими деревьями исходников: 10–200 тыс. файлов и частые мелкие правки. Заменяет решения вроде dklab RealSync без Cygwin, Perl и rsync.

- Один `owysync.exe`, на сервер ничего не устанавливается: используются SSH, SFTP и стандартные утилиты Unix.
- Каждый запуск — полный resync; на 100 тыс. неизменённых файлов это несколько секунд. Затем live-режим доставляет каждый сохранённый файл примерно за секунду.
- Массовые изменения (`git checkout`, первая заливка) автоматически идут через tar-архивы на полной скорости канала.
- Безопасность по умолчанию: не синхронизирует в каталог без файла-маркера, не удаляет сокеты на сервере, не переходит по удалённым symlink.

### Быстрый старт

```bat
cd C:\work\myproject
owysync init
```

Проверьте `.owysync\ignore`: первая синхронизация удаляет на сервере всё, чего нет в локальном каталоге и что не исключено. Затем посмотрите план и запустите:

```bat
owysync sync --once --dry-run
owysync sync
```

### Документация

- [Руководство пользователя](docs/user-guide.ru.md)

### Лицензия

[MIT](LICENSE)
