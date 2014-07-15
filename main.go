// gocv is a fork of https://github.com/Xfennec/cv, the Coreutils Viewer
//
// I like the tool very much but I didn't want to have it in c, so i rewrote it in go
//
// TODO:
//
// 1) somehow I get two results for each process that is found (input and output)
package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/codegangsta/cli"
	"github.com/dustin/go-humanize"
)

const (
	procPath = "/proc"
)

type procInfo struct {
	Name string
	PID  int
}

type fdInfo struct {
	FdNum     int
	Size, Pos int64
	Name      string
	Now       time.Time
}

type result struct {
	pid procInfo
	fd  fdInfo
}

var (
	procNames = []string{"cp", "mv", "dd", "tar", "gzip", "gunzip", "cat", "grep", "fgrep", "egrep", "cut", "sort"}
)

func main() {
	app := cli.NewApp()

	app.Name = "gocv"
	app.Version = "0.0.1"
	app.Usage = "GOreutils Viewer"

	app.Flags = []cli.Flag{
		cli.BoolFlag{Name: "debug,d", Usage: "output debug information"},
		cli.BoolFlag{Name: "wait,w", Usage: "estimate I/O throughput (slower display)"},
		cli.IntFlag{Name: "wait-delay,W", Value: 1, Usage: "wait 'secs' seconds for I/O estimation (implies -w)"},
		cli.StringSliceFlag{Name: "command,c", Value: new(cli.StringSlice), Usage: "monitor only this command name (ex: firefox)"},
	}
	app.Action = run

	app.Run(os.Args)
}

func run(c *cli.Context) {
	var (
		maxSize   int64
		info      *fdInfo
		biggestFd fdInfo
		results   []result
	)

	if !c.Bool("debug") {
		log.SetOutput(ioutil.Discard)
	}

	cmds := c.StringSlice("command")
	if len(cmds) == 0 {
		cmds = procNames
	}

	procs := findPidsByBinName(cmds)

	log.Println("Relevent PIDs:", procs)

	for _, proc := range procs {
		fds := findFdForPid(proc.PID)
		log.Printf("FDs for [%s,%5d]: %v\n", proc.Name, proc.PID, fds)

		maxSize = 0

		for _, fd := range fds {
			info = getFdInfo(proc.PID, fd)
			if info == nil {
				continue
			}
			log.Printf("fdInfo for [%5d,%5d]: %v\n", proc.PID, fd, info)

			if info.Size > maxSize {
				biggestFd = *info
				maxSize = info.Size
			}

			if maxSize == 0 { // nothing found
				log.Printf("[%5d] %s inactive/flushing/streaming/...\n", proc.PID, proc.Name)
				continue
			}

			results = append(results, result{proc, biggestFd})
		}
	}

	if len(results) == 0 {
		os.Exit(0)
	}
	var doWait bool = c.Bool("wait")
	if doWait {
		time.Sleep(time.Second * time.Duration(c.Int("wait-delay")))
	}

	var (
		stillThere  bool
		fpos, fsize string
		perc        float64
	)
	for _, res := range results {
		stillThere = false
		if doWait {
			info = getFdInfo(res.pid.PID, res.fd.FdNum)
			if info != nil {
				stillThere = true
			}

			if stillThere {
				log.Println("Still there..!")
				if info.Name != res.fd.Name {
					log.Printf("%s != %s\n", info.Name, res.fd.Name)
					stillThere = false // still there, but it's not the same file !
				}
			}
		}

		if stillThere {
			// use the newest info
			fpos = humanize.Bytes(uint64(info.Pos))
			fsize = humanize.Bytes(uint64(info.Size))
			perc = 100.0 * float64(info.Pos) / float64(info.Size)
		} else {
			// pid is no more here (or no throughput was asked), use initial info
			fpos = humanize.Bytes(uint64(res.fd.Pos))
			fsize = humanize.Bytes(uint64(res.fd.Size))
			perc = 100.0 * float64(res.fd.Pos) / float64(res.fd.Size)
		}

		fmt.Printf("[%5d] %s %s %.1f%% (%s / %s)",
			res.pid.PID,
			res.pid.Name,
			res.fd.Name,
			perc,
			fpos,
			fsize)

		if doWait && stillThere {
			tDiff := info.Now.Sub(res.fd.Now)
			bDiff := float64(info.Pos - res.fd.Pos)

			bps := bDiff / tDiff.Seconds()
			fmt.Printf(" %s/s", humanize.Bytes(uint64(bps)))
		}

		fmt.Println()

	}
}

// step 1 - find all pids of the specified binarys in the binNames array
func findPidsByBinName(binNames []string) (pids []procInfo) {
	procDir, err := os.Open(procPath)
	check(err)
	defer procDir.Close()

	procEntries, err := procDir.Readdirnames(-1)
	check(err)

	for _, entry := range procEntries {
		pid, err := strconv.Atoi(entry)
		if err == nil {
			execPath := fmt.Sprintf("%s/%s/exe", procPath, entry)

			linkDest, err := os.Readlink(execPath)
			if pErr, ok := err.(*os.PathError); ok {
				if pErr.Err.Error() == "permission denied" {
					// don't crash over permission error
					continue
				}
			} else {
				check(err)
			}

			exec := filepath.Base(linkDest)
			for _, binName := range binNames {
				if exec == binName {
					log.Printf("Found relevant process of [%10s] pid[%5d]\n", binName, pid)
					pids = append(pids, procInfo{Name: binName, PID: pid})
					break
				}
			}
		}
	}
	return
}

// step 2 - find all file descriptors of pid belonging to regular or block devices
func findFdForPid(pid int) (fds []int) {
	path := fmt.Sprintf("%s/%d/fd", procPath, pid)
	fdDir, err := os.Open(path)
	check(err)
	defer fdDir.Close()

	procEntries, err := fdDir.Readdirnames(-1)
	check(err)

	for _, entry := range procEntries {
		fdPath := fmt.Sprintf("%s/%s", path, entry)
		stat, err := os.Stat(fdPath)
		check(err)

		// TODO: add block device
		if !stat.Mode().IsRegular() {
			continue
		}

		_, err = os.Readlink(fdPath)
		check(err)

		fdInt, _ := strconv.Atoi(entry)
		fds = append(fds, fdInt)
	}

	return
}

// step 3 - get positions inside the filedescriptor for (pid,fd)
func getFdInfo(pid, fd int) *fdInfo {
	var (
		err  error
		info fdInfo
	)

	info.FdNum = fd
	path := fmt.Sprintf("%s/%d/fd/%d", procPath, pid, fd)

	info.Name, err = os.Readlink(path)
	check(err)

	stat, err := os.Stat(info.Name)
	if pErr, ok := err.(*os.PathError); ok {
		if pErr.Err.Error() == "no such file or directory" {
			// might be dead link
			log.Println("No such file for ", info.Name)
			return nil
		}
	} else {
		check(err)
	}

	// TODO add block device
	info.Size = stat.Size()

	infoPath := fmt.Sprintf("%s/%d/fdinfo/%d", procPath, pid, fd)

	// not sure why mode should be "rt"
	infoFile, err := os.Open(infoPath)
	if pErr, ok := err.(*os.PathError); ok {
		if pErr.Err.Error() == "permission denied" {
			log.Println("permission denied for", infoPath)
			return nil
		}
	} else {
		check(err)
	}

	info.Now = time.Now()

	infoContent, err := ioutil.ReadAll(infoFile)
	check(err)

	lines := bytes.Split(infoContent, []byte("\n"))
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("pos:")) {
			info.Pos, err = strconv.ParseInt(string(line[5:]), 10, 64)
			check(err)
		}
	}

	return &info
}

func check(err error) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		log.Fatalf("Fatal from <%s:%d>\nError:%s", file, line-1, err)
	}
}
