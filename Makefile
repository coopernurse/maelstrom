.PHONY: test watch-test maelctl maelstromd idl run-maelstromd cover
.EXPORT_ALL_VARIABLES:

GO111MODULE = on
MAEL_SQL_DRIVER = sqlite3
MAEL_SQL_DSN = ./tmp/maelstrom.db?cache=shared&_journal_mode=MEMORY
#MAEL_SQL_DRIVER = postgres
#MAEL_SQL_DSN = postgres://postgres:test@localhost:5432/mael?sslmode=disable
MAEL_PUBLIC_PORT = 8008

BUILD_VER = 0.0.0
BUILD_DATE := $(shell date +%FT%T%z)
BUILD_GITSHA := $(shell git rev-parse --short HEAD)
LD_FLAGS = -ldflags "-X main.version=$(BUILD_VER) -X main.builddate=$(BUILD_DATE) -X main.gitsha=$(BUILD_GITSHA)"

test:
	scripts/gofmt_check.sh
	rm -f pkg/v1/test.db pkg/gateway/test.db
	go test -timeout 4m ./...
	errcheck -ignore 'fmt:[FS]?[Pp]rint*' ./...

watch-test:
	find . -name *.go | entr -c make test

cover:
	mkdir -p tmp
	go test -coverprofile=tmp/cover.out github.com/coopernurse/maelstrom/pkg/maelstrom \
	    github.com/coopernurse/maelstrom/pkg/maelstrom/component \
	    github.com/coopernurse/maelstrom/pkg/common
	go tool cover -html=tmp/cover.out

maelctl:
	go build ${LD_FLAGS} -o dist/maelctl --tags "libsqlite3 linux" cmd/maelctl/*.go

maelstromd:
	go build ${LD_FLAGS} -o dist/maelstromd --tags "libsqlite3 linux" cmd/maelstromd/*.go

idl:
	barrister idl/maelstrom.idl | idl2go -i -p v1 -d pkg
	gofmt -w pkg/v1/*.go

docker-image:
	docker build -t coopernurse/maelstrom .

docker-run-maelstromd:
	mkdir -p tmp
	docker run -d --name maelstromd -p 8374:8374 -p 8008:8008 -v `pwd`/tmp:/data --privileged \
	  -v /var/run/docker.sock:/var/run/docker.sock \
	  -e MAEL_SQL_DRIVER="sqlite3" \
	  -e MAEL_PUBLIC_PORT=8008 \
	  -e MAEL_SQL_DSN="/data/maelstrom.db?cache=shared&_journal_mode=MEMORY"  \
	  coopernurse/maelstrom maelstromd

docker-push-image:
	docker tag coopernurse/maelstrom docker.io/coopernurse/maelstrom
	docker push docker.io/coopernurse/maelstrom

run-maelstromd:
	mkdir -p tmp
	./dist/maelstromd &

profile-maelstromd:
	mkdir -p tmp
	./dist/maelstromd &

copy-to-server:
	scp ./dist/maelstromd root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/linux_x86_64/
	scp ./dist/maelctl root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/linux_x86_64/

copy-aws-scripts-to-server:
	scp ./cloud/aws/mael-init-node.sh root@maelstromapp.com:/opt/web/sites/download.maelstromapp.com/latest/

gitbook:
	cd docs/gitbook && gitbook build

publish-web:
	make gitbook
	rm -rf docs/maelstromapp.com/docs/
	cp -r docs/gitbook/_book docs/maelstromapp.com/docs
	rsync -avz docs/maelstromapp.com/ root@maelstromapp.com:/opt/web/sites/maelstromapp.com/
