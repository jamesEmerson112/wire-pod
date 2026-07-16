package server

import (
	"context"
	"strconv"
	"time"

	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

const (
	connectionCheckTimeout = 15 * time.Second
	check                  = "check"
)

// StreamingConnectionCheck is used by the end device to make sure it can successfully communicate
func (s *Server) StreamingConnectionCheck(stream pb.ChipperGrpc_StreamingConnectionCheckServer) error {
	req, err := stream.Recv()
	logger.Debug("conn", req.DeviceId, "incoming connection check")
	if err != nil {
		logger.Error("conn", req.DeviceId, "conn check error: "+err.Error())
		return err
	}
	deviceId := req.DeviceId

	ctx, cancel := context.WithTimeout(stream.Context(), connectionCheckTimeout)
	defer cancel()

	framesPerRequest := req.TotalAudioMs / req.AudioPerRequest

	var toSend pb.ConnectionCheckResponse

	// count frames, we already pulled the first one
	frames := uint32(1)
	toSend.FramesReceived = frames
receiveLoop:
	for {
		select {
		case <-ctx.Done():
			logger.Debug("conn", deviceId, "expired, frames received "+strconv.Itoa(int(frames)))
			toSend.Status = "Timeout"
			break receiveLoop
		default:
			req, suberr := stream.Recv()

			if suberr != nil || req == nil {
				err = suberr
				logger.Error("conn", deviceId, "conn check error: "+err.Error())

				toSend.Status = "Error"
				break receiveLoop
			}

			frames++
			toSend.FramesReceived = frames
			if frames >= framesPerRequest {
				logger.Debug("conn", deviceId, "success")
				toSend.Status = "Success"
				break receiveLoop
			}
		}
	}
	senderr := stream.Send(&toSend)
	if senderr != nil {
		logger.Error("conn", deviceId, "failed to send response")
		return senderr
	}
	return err

}
