package sfu

import (
	"io"
	"math"
	"time"

	"github.com/pion/ion-sfu/pkg/log"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v2"
)

// SenderConfig describes configuration of a sender
type SenderConfig struct {
	REMBFeedback bool
	MinBandwidth uint64
	MaxBandwidth uint64
}

// Sender represents a track being sent to a peer
type Sender struct {
	track    *webrtc.Track
	stop     bool
	rtpCh    chan *rtp.Packet
	rtcpCh   chan rtcp.Packet
	useRemb  bool
	rembChan chan *rtcp.ReceiverEstimatedMaximumBitrate
}

// NewSender creates a new send track instance
func NewSender(track *webrtc.Track, trans *webrtc.RTPTransceiver) *Sender {
	s := &Sender{
		track: track,
	}

	for _, feedback := range track.Codec().RTCPFeedback {
		switch feedback.Type {
		case webrtc.TypeRTCPFBGoogREMB:
			s.useRemb = true
		case webrtc.TypeRTCPFBTransportCC:
			// TODO
		}
	}

	go s.receiveRTCP(trans.Sender())

	return s
}

// WriteRTP to the track
func (s *Sender) WriteRTP(pkt *rtp.Packet) error {
	// Transform payload type
	pt := s.track.Codec().PayloadType
	newPkt := *pkt
	newPkt.Header.PayloadType = pt
	pkt = &newPkt
	return s.track.WriteRTP(pkt)
}

// Close track
func (s *Sender) Close() {
	s.stop = true
}

func (s *Sender) receiveRTCP(sender *webrtc.RTPSender) {
	for {
		pkts, err := sender.ReadRTCP()
		if err == io.EOF || err == io.ErrClosedPipe {
			return
		}

		if err != nil {
			log.Errorf("rtcp err => %v", err)
		}

		if s.stop {
			return
		}

		for _, pkt := range pkts {
			switch pkt := pkt.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest, *rtcp.TransportLayerNack:
				s.rtcpCh <- pkt
			case *rtcp.ReceiverEstimatedMaximumBitrate:
				if s.useRemb {
					s.rembChan <- pkt
				}
			default:
			}
		}
	}
	log.Infof("Closing sub feedback")
}

func (s *Sender) rembLoop() {
	lastRembTime := time.Now()
	maxRembTime := 200 * time.Millisecond
	rembMin := uint64(100000)
	rembMax := uint64(5000000)
	if rembMin == 0 {
		rembMin = 10000 // 10 KBit
	}
	if rembMax == 0 {
		rembMax = 100000000 // 100 MBit
	}
	var lowest uint64 = math.MaxUint64
	var rembCount, rembTotalRate uint64

	for pkt := range s.rembChan {
		// Update stats
		rembCount++
		rembTotalRate += pkt.Bitrate
		if pkt.Bitrate < lowest {
			lowest = pkt.Bitrate
		}

		// Send upstream if time
		if time.Since(lastRembTime) > maxRembTime {
			lastRembTime = time.Now()
			avg := uint64(rembTotalRate / rembCount)

			_ = avg
			target := lowest

			if target < rembMin {
				target = rembMin
			} else if target > rembMax {
				target = rembMax
			}

			newPkt := &rtcp.ReceiverEstimatedMaximumBitrate{
				Bitrate:    target,
				SenderSSRC: 1,
				SSRCs:      pkt.SSRCs,
			}

			log.Infof("Router.rembLoop send REMB: %+v", newPkt)

			s.rtcpCh <- newPkt

			// Reset stats
			rembCount = 0
			rembTotalRate = 0
			lowest = math.MaxUint64
		}
	}
}
