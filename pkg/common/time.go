package common

import "time"

func NowMillis() int64 {
	return time.Now().UnixNano() / 1e6
}
