package webconsole

import (
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

type overviewPage struct {
	Section, OS, Arch, GoVersion, Time string
	CPUs                               int
	Build                              Build
	Ready                              bool
	Heap, Disk                         capacityReading
	Database                           sql.DBStats
	SQLiteVersion                      string
}

func (c *console) overview(w http.ResponseWriter, r *http.Request, who identity) {
	data := overviewPage{Build: c.Build, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), Time: time.Now().UTC().Format(time.RFC3339)}
	switch r.URL.Query().Get("section") {
	case "database":
		data.Section = "database"
		db := c.Store.DB()
		if err := db.QueryRowContext(r.Context(), `SELECT sqlite_version()`).Scan(&data.SQLiteVersion); err != nil {
			data.SQLiteVersion = "未知"
		}
		data.Database = db.Stats()
	case "build":
		data.Section = "build"
	default:
		data.Section = "status"
		data.Ready = c.Store.CheckReady(r.Context()) == nil
		data.CPUs = runtime.NumCPU()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		data.Heap = capacityReading{Value: mem.HeapAlloc, Total: mem.HeapSys}
		if free, total, err := c.Store.DiskCapacity(); err == nil {
			data.Disk = capacityReading{Value: free, Total: total}
		}
	}
	c.show(w, r, who, http.StatusOK, "服务概览", "overview", data, "")
}

type capacityReading struct{ Value, Total uint64 }

func (r capacityReading) Percent() string {
	return fmt.Sprintf("%.1f%%", 100*float64(r.Value)/float64(r.Total))
}

func byteSize(n uint64) string {
	size := float64(n)
	for _, unit := range []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		if size < 1024 {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
		size /= 1024
	}
	return "未知"
}
