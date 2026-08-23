// Package probe is direct cgo bindings to libavformat/libavcodec for
// duration and keyframe detection (ADR 0002), replacing a step that
// previously shelled out to ffprobe. Only wraps the two operations
// transcode.EncodeHLS's segment-boundary planning needs, not all of
// libavformat.
package probe

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <stdlib.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libavutil/avutil.h>
*/
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

// Duration opens path and returns the container's duration, the same
// value ffprobe's "format.duration" surfaces.
func Duration(path string) (time.Duration, error) {
	fmtCtx, err := openInput(path)
	if err != nil {
		return 0, err
	}
	defer C.avformat_close_input(&fmtCtx)

	if fmtCtx.duration == C.AV_NOPTS_VALUE {
		return 0, fmt.Errorf("probe: %s: container reports no duration", path)
	}
	// AVFormatContext.duration is in AV_TIME_BASE units (microseconds).
	return time.Duration(int64(fmtCtx.duration)) * time.Microsecond, nil
}

// Keyframe is one keyframe (IDR/sync frame) location in a stream, as a
// presentation timestamp relative to the start of the file.
type Keyframe struct {
	PTS time.Duration
}

// Keyframes opens path and returns the presentation timestamp of every
// packet in its primary video stream flagged as a keyframe.
func Keyframes(path string) ([]Keyframe, error) {
	fmtCtx, err := openInput(path)
	if err != nil {
		return nil, err
	}
	defer C.avformat_close_input(&fmtCtx)

	streamIdx := C.av_find_best_stream(fmtCtx, C.AVMEDIA_TYPE_VIDEO, -1, -1, nil, 0)
	if streamIdx < 0 {
		return nil, fmt.Errorf("probe: %s: no video stream found", path)
	}
	// fmtCtx.streams is a C array of AVStream*, nb_streams long;
	// unsafe.Slice turns it into a normal Go slice for indexing.
	streams := unsafe.Slice(fmtCtx.streams, int(fmtCtx.nb_streams))
	timeBase := streams[streamIdx].time_base

	pkt := C.av_packet_alloc()
	if pkt == nil {
		return nil, fmt.Errorf("probe: allocating packet failed")
	}
	defer C.av_packet_free(&pkt)

	var keyframes []Keyframe
	for {
		ret := C.av_read_frame(fmtCtx, pkt)
		if ret < 0 {
			break // EOF or read error; either way, nothing more to walk
		}
		if pkt.stream_index == streamIdx && (pkt.flags&C.AV_PKT_FLAG_KEY) != 0 && pkt.pts != C.AV_NOPTS_VALUE {
			seconds := float64(C.av_q2d(timeBase)) * float64(pkt.pts)
			keyframes = append(keyframes, Keyframe{PTS: time.Duration(seconds * float64(time.Second))})
		}
		C.av_packet_unref(pkt)
	}

	if len(keyframes) == 0 {
		return nil, fmt.Errorf("probe: %s: no keyframes found in video stream", path)
	}
	return keyframes, nil
}

// VideoInfo is basic geometry/bitrate info for a container's primary
// video stream.
type VideoInfo struct {
	Width, Height int
	BitrateKbps   int // 0 if neither the stream nor the container reports one
}

// Video opens path and returns its primary video stream's dimensions and
// bitrate — what transcode.Params needs to match an encode to an
// arbitrary input.
func Video(path string) (VideoInfo, error) {
	fmtCtx, err := openInput(path)
	if err != nil {
		return VideoInfo{}, err
	}
	defer C.avformat_close_input(&fmtCtx)

	streamIdx := C.av_find_best_stream(fmtCtx, C.AVMEDIA_TYPE_VIDEO, -1, -1, nil, 0)
	if streamIdx < 0 {
		return VideoInfo{}, fmt.Errorf("probe: %s: no video stream found", path)
	}
	streams := unsafe.Slice(fmtCtx.streams, int(fmtCtx.nb_streams))
	params := streams[streamIdx].codecpar

	// Per-stream bitrate is commonly unset for some containers/codecs;
	// fall back to the container-level bitrate (covers the whole file,
	// audio included, but is a reasonable estimate when nothing more
	// precise is available) rather than reporting a hard zero.
	bitrate := int64(params.bit_rate)
	if bitrate == 0 {
		bitrate = int64(fmtCtx.bit_rate)
	}

	return VideoInfo{
		Width:       int(params.width),
		Height:      int(params.height),
		BitrateKbps: int(bitrate / 1000),
	}, nil
}

// openInput opens and probes path, the shared first step Duration and
// Keyframes both need. Callers must avformat_close_input the result.
func openInput(path string) (*C.AVFormatContext, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var fmtCtx *C.AVFormatContext
	if ret := C.avformat_open_input(&fmtCtx, cPath, nil, nil); ret < 0 {
		return nil, fmt.Errorf("probe: opening %s: %w", path, avError(ret))
	}
	if ret := C.avformat_find_stream_info(fmtCtx, nil); ret < 0 {
		C.avformat_close_input(&fmtCtx)
		return nil, fmt.Errorf("probe: reading stream info for %s: %w", path, avError(ret))
	}
	return fmtCtx, nil
}

// avError renders an AVERROR code the way FFmpeg's own tools do, instead
// of surfacing a bare negative int.
func avError(code C.int) error {
	buf := make([]C.char, C.AV_ERROR_MAX_STRING_SIZE)
	C.av_strerror(code, &buf[0], C.AV_ERROR_MAX_STRING_SIZE)
	return fmt.Errorf("%s", C.GoString(&buf[0]))
}
