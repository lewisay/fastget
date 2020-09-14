package fastget

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"time"

	"golang.org/x/net/context/ctxhttp"
	"golang.org/x/sync/errgroup"
)

// FastGetter Represents the information required to fastget a file url
type FastGetter struct {
	FileURL    string
	Workers    int
	OutputFile string
	// Headers to be included to while making requests
	Headers map[string]string
	// OnBeforeStart to be called before even download start
	OnBeforeStart func(int64, int64)

	// OnStart to be called on started downloading a chunk / a part
	OnStart func(int, int64)
	// OnProgress to be called on change in progress of downloading a chunk / a part
	OnProgress func(int, int64)
	// OnFinish to be called on finished downloading a chunk / a part
	OnFinish func(int)
}

type chunkInfo struct {
	ctx      context.Context
	client   *http.Client
	output   io.WriterAt
	url      string
	off, lim int64
	wid      int
	headers  map[string]string
}

// Result represents the result of fastget
type Result struct {
	FileURL     string
	Size        int64
	OutputFile  *os.File
	ElapsedTime time.Duration
}

// NewFastGetter creates and returns an instance of FastGetter
func NewFastGetter(fileURL string) (*FastGetter, error) {
	fg := &FastGetter{
		FileURL:    fileURL,
		Workers:    3,
		OutputFile: path.Base(fileURL),
		Headers:    make(map[string]string),
	}
	return fg, nil
}

// Get ultrafast downloads the file
func (fg *FastGetter) Get() (*Result, error) {
	return fg.get()
}

func (fg *FastGetter) get() (*Result, error) {
	canFastGet, length, err := fg.checkEligibility()
	if err != nil {
		return nil, err
	}
	if !canFastGet {
		// warn
		fmt.Println("WARN: FileURL doesn't support parellel download.")
		fg.Workers = 1
	}

	chunkLen := int64(length / int64(fg.Workers))

	if fg.OnBeforeStart != nil {
		fg.OnBeforeStart(length, chunkLen)
	}

	ctx := context.Background()
	client := http.DefaultClient

	output, err := os.OpenFile(fg.OutputFile, os.O_CREATE|os.O_RDWR, 0666)

	if err != nil {
		return nil, err
	}

	wg, ctx := errgroup.WithContext(ctx)

	startTime := time.Now()

	var start, end int64
	for wid := 1; wid <= fg.Workers; wid++ {

		if wid == fg.Workers {
			end = length // last part
		} else {
			end = start + chunkLen
		}

		wid := wid
		off := start
		lim := end

		wg.Go(func() error {
			return fg.getChunk(&chunkInfo{
				ctx:     ctx,
				client:  client,
				output:  output,
				url:     fg.FileURL,
				off:     off,
				lim:     lim,
				wid:     wid,
				headers: fg.Headers,
			})
		})

		start = end
	}

	err = wg.Wait()
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(startTime)

	result := &Result{
		FileURL:     fg.FileURL,
		Size:        length,
		OutputFile:  output,
		ElapsedTime: elapsed,
	}
	return result, nil
}

func (fg FastGetter) checkEligibility() (bool, int64, error) {
	req, err := http.NewRequest("HEAD", fg.FileURL, nil)
	if err != nil {
		return false, 0, err
	}
	for key, value := range fg.Headers {
		req.Header.Add(key, value)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, 0, err
	}
	acceptRanges := res.Header.Get("Accept-Ranges") == "bytes"
	length := res.ContentLength

	return acceptRanges, length, nil
}

func (cInfo chunkInfo) makeRequest() (*http.Response, error) {
	req, err := http.NewRequest("GET", cInfo.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", cInfo.off, cInfo.lim))
	// Add custom headers
	for key, value := range cInfo.headers {
		req.Header.Add(key, value)
	}

	resp, err := ctxhttp.Do(cInfo.ctx, cInfo.client, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("server responded with %d status code, expected %d", resp.StatusCode, http.StatusPartialContent)
	}
	return resp, nil
}

func (fg FastGetter) getChunk(cInfo *chunkInfo) error {
	if fg.OnStart != nil {
		fg.OnStart(cInfo.wid, cInfo.lim-cInfo.off)
	}
	resp, err := cInfo.makeRequest()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var written int64
	contentLen := resp.ContentLength

	buf := make([]byte, 1*1024*1024)
	for {
		nr, er := resp.Body.Read(buf)

		if nr > 0 {
			nw, err := cInfo.output.WriteAt(buf[0:nr], cInfo.off)
			if err != nil {
				return fmt.Errorf("error writing chunk. %s", err.Error())
			}
			if nr != nw {
				return fmt.Errorf("error writing chunk. written %d, but expected %d", nw, nr)
			}

			cInfo.off = int64(nw) + cInfo.off
			if nw > 0 {
				written += int64(nw)
			}
			if fg.OnProgress != nil {
				fg.OnProgress(cInfo.wid, written)
			}
		}

		if er != nil {
			if er.Error() == "EOF" {
				if contentLen == written {
					// Download successfully
				} else {
					return fmt.Errorf("error reading response. %s", er.Error())
				}
				break
			}
			return er
		}
	}
	if fg.OnFinish != nil {
		fg.OnFinish(cInfo.wid)
	}
	return nil
}
