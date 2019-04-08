package ackhandler

import (
	"time"

	"github.com/golang/mock/gomock"
	"github.com/lucas-clemente/quic-go/internal/congestion"
	"github.com/lucas-clemente/quic-go/internal/mocks"
	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/utils"
	"github.com/lucas-clemente/quic-go/internal/wire"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func retransmittablePacket(p *Packet) *Packet {
	if p.EncryptionLevel == protocol.EncryptionUnspecified {
		p.EncryptionLevel = protocol.Encryption1RTT
	}
	if p.Length == 0 {
		p.Length = 1
	}
	if p.SendTime.IsZero() {
		p.SendTime = time.Now()
	}
	p.Frames = []wire.Frame{&wire.PingFrame{}}
	return p
}

func nonRetransmittablePacket(p *Packet) *Packet {
	p = retransmittablePacket(p)
	p.Frames = []wire.Frame{
		&wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 1}}},
	}
	return p
}

func cryptoPacket(p *Packet) *Packet {
	p = retransmittablePacket(p)
	p.EncryptionLevel = protocol.EncryptionInitial
	return p
}

var _ = Describe("SentPacketHandler", func() {
	var (
		handler     *sentPacketHandler
		streamFrame wire.StreamFrame
	)

	BeforeEach(func() {
		rttStats := &congestion.RTTStats{}
		handler = NewSentPacketHandler(42, rttStats, utils.DefaultLogger).(*sentPacketHandler)
		handler.SetHandshakeComplete()
		streamFrame = wire.StreamFrame{
			StreamID: 5,
			Data:     []byte{0x13, 0x37},
		}
	})

	getPacket := func(pn protocol.PacketNumber, encLevel protocol.EncryptionLevel) *Packet {
		if el, ok := handler.getPacketNumberSpace(encLevel).history.packetMap[pn]; ok {
			return &el.Value
		}
		return nil
	}

	losePacket := func(pn protocol.PacketNumber, encLevel protocol.EncryptionLevel) {
		p := getPacket(pn, encLevel)
		ExpectWithOffset(1, p).ToNot(BeNil())
		handler.queuePacketForRetransmission(p, handler.getPacketNumberSpace(encLevel))
		if p.includedInBytesInFlight {
			p.includedInBytesInFlight = false
			handler.bytesInFlight -= p.Length
		}
		r := handler.DequeuePacketForRetransmission()
		ExpectWithOffset(1, r).ToNot(BeNil())
		ExpectWithOffset(1, r.PacketNumber).To(Equal(pn))
	}

	expectInPacketHistory := func(expected []protocol.PacketNumber, encLevel protocol.EncryptionLevel) {
		pnSpace := handler.getPacketNumberSpace(encLevel)
		ExpectWithOffset(1, pnSpace.history.Len()).To(Equal(len(expected)))
		for _, p := range expected {
			ExpectWithOffset(1, pnSpace.history.packetMap).To(HaveKey(p))
		}
	}

	updateRTT := func(rtt time.Duration) {
		handler.rttStats.UpdateRTT(rtt, 0, time.Now())
		ExpectWithOffset(1, handler.rttStats.SmoothedRTT()).To(Equal(rtt))
	}

	Context("registering sent packets", func() {
		It("accepts two consecutive packets", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, EncryptionLevel: protocol.EncryptionHandshake}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, EncryptionLevel: protocol.EncryptionHandshake}))
			Expect(handler.handshakePackets.largestSent).To(Equal(protocol.PacketNumber(2)))
			expectInPacketHistory([]protocol.PacketNumber{1, 2}, protocol.EncryptionHandshake)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(2)))
		})

		It("accepts packet number 0", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 0, EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.oneRTTPackets.largestSent).To(BeZero())
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.oneRTTPackets.largestSent).To(Equal(protocol.PacketNumber(1)))
			expectInPacketHistory([]protocol.PacketNumber{0, 1}, protocol.Encryption1RTT)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(2)))
		})

		It("stores the sent time", func() {
			sendTime := time.Now().Add(-time.Minute)
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: sendTime}))
			Expect(handler.lastSentRetransmittablePacketTime).To(Equal(sendTime))
		})

		It("stores the sent time of crypto packets", func() {
			sendTime := time.Now().Add(-time.Minute)
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: sendTime, EncryptionLevel: protocol.EncryptionInitial}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: sendTime.Add(time.Hour), EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.lastSentCryptoPacketTime).To(Equal(sendTime))
		})

		It("does not store non-retransmittable packets", func() {
			handler.SentPacket(nonRetransmittablePacket(&Packet{PacketNumber: 1, EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.oneRTTPackets.history.Len()).To(BeZero())
			Expect(handler.lastSentRetransmittablePacketTime).To(BeZero())
			Expect(handler.bytesInFlight).To(BeZero())
		})
	})

	Context("ACK processing", func() {
		BeforeEach(func() {
			for i := protocol.PacketNumber(0); i < 10; i++ {
				handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: i}))
			}
			// Increase RTT, because the tests would be flaky otherwise
			updateRTT(time.Hour)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(10)))
		})

		Context("ACK validation", func() {
			It("accepts ACKs sent in packet 0", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 5}}}
				err := handler.ReceivedAck(ack, 0, protocol.Encryption1RTT, time.Now())
				Expect(err).ToNot(HaveOccurred())
				Expect(handler.oneRTTPackets.largestAcked).To(Equal(protocol.PacketNumber(5)))
			})

			It("accepts multiple ACKs sent in the same packet", func() {
				ack1 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 3}}}
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 4}}}
				Expect(handler.ReceivedAck(ack1, 1337, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.oneRTTPackets.largestAcked).To(Equal(protocol.PacketNumber(3)))
				// this wouldn't happen in practice
				// for testing purposes, we pretend send a different ACK frame in a duplicated packet, to be able to verify that it actually doesn't get processed
				Expect(handler.ReceivedAck(ack2, 1337, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.oneRTTPackets.largestAcked).To(Equal(protocol.PacketNumber(4)))
			})

			It("rejects ACKs with a too high LargestAcked packet number", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 9999}}}
				err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
				Expect(err).To(MatchError("PROTOCOL_VIOLATION: Received ACK for an unsent packet"))
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(10)))
			})

			It("ignores repeated ACKs", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 3}}}
				Expect(handler.ReceivedAck(ack, 1337, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(7)))
				Expect(handler.ReceivedAck(ack, 1337+1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.oneRTTPackets.largestAcked).To(Equal(protocol.PacketNumber(3)))
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(7)))
			})
		})

		Context("acks and nacks the right packets", func() {
			It("adjusts the LargestAcked, and adjusts the bytes in flight", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 5}}}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.oneRTTPackets.largestAcked).To(Equal(protocol.PacketNumber(5)))
				expectInPacketHistory([]protocol.PacketNumber{6, 7, 8, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(4)))
			})

			It("acks packet 0", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 0, Largest: 0}}}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(getPacket(0, protocol.Encryption1RTT)).To(BeNil())
				expectInPacketHistory([]protocol.PacketNumber{1, 2, 3, 4, 5, 6, 7, 8, 9}, protocol.Encryption1RTT)
			})

			It("handles an ACK frame with one missing packet range", func() {
				ack := &wire.AckFrame{ // lose 4 and 5
					AckRanges: []wire.AckRange{
						{Smallest: 6, Largest: 9},
						{Smallest: 1, Largest: 3},
					},
				}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 4, 5}, protocol.Encryption1RTT)
			})

			It("does not ack packets below the LowestAcked", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 3, Largest: 8}}}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 1, 2, 9}, protocol.Encryption1RTT)
			})

			It("handles an ACK with multiple missing packet ranges", func() {
				ack := &wire.AckFrame{ // packets 2, 4 and 5, and 8 were lost
					AckRanges: []wire.AckRange{
						{Smallest: 9, Largest: 9},
						{Smallest: 6, Largest: 7},
						{Smallest: 3, Largest: 3},
						{Smallest: 1, Largest: 1},
					},
				}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 2, 4, 5, 8}, protocol.Encryption1RTT)
			})

			It("processes an ACK frame that would be sent after a late arrival of a packet", func() {
				ack1 := &wire.AckFrame{ // 3 lost
					AckRanges: []wire.AckRange{
						{Smallest: 4, Largest: 6},
						{Smallest: 1, Largest: 2},
					},
				}
				Expect(handler.ReceivedAck(ack1, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 3, 7, 8, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(5)))
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 6}}} // now ack 3
				Expect(handler.ReceivedAck(ack2, 2, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 7, 8, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(4)))
			})

			It("processes an ACK frame that would be sent after a late arrival of a packet and another packet", func() {
				ack1 := &wire.AckFrame{
					AckRanges: []wire.AckRange{
						{Smallest: 4, Largest: 6},
						{Smallest: 0, Largest: 2},
					},
				}
				Expect(handler.ReceivedAck(ack1, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{3, 7, 8, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(4)))
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 7}}}
				Expect(handler.ReceivedAck(ack2, 2, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(2)))
				expectInPacketHistory([]protocol.PacketNumber{8, 9}, protocol.Encryption1RTT)
			})

			It("processes an ACK that contains old ACK ranges", func() {
				ack1 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 6}}}
				Expect(handler.ReceivedAck(ack1, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 7, 8, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(4)))
				ack2 := &wire.AckFrame{
					AckRanges: []wire.AckRange{
						{Smallest: 8, Largest: 8},
						{Smallest: 3, Largest: 3},
						{Smallest: 1, Largest: 1},
					},
				}
				Expect(handler.ReceivedAck(ack2, 2, protocol.Encryption1RTT, time.Now())).To(Succeed())
				expectInPacketHistory([]protocol.PacketNumber{0, 7, 9}, protocol.Encryption1RTT)
				Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(3)))
			})
		})

		Context("calculating RTT", func() {
			It("computes the RTT", func() {
				now := time.Now()
				// First, fake the sent times of the first, second and last packet
				getPacket(1, protocol.Encryption1RTT).SendTime = now.Add(-10 * time.Minute)
				getPacket(2, protocol.Encryption1RTT).SendTime = now.Add(-5 * time.Minute)
				getPacket(6, protocol.Encryption1RTT).SendTime = now.Add(-1 * time.Minute)
				// Now, check that the proper times are used when calculating the deltas
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 1}}}
				err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
				Expect(err).NotTo(HaveOccurred())
				Expect(handler.rttStats.LatestRTT()).To(BeNumerically("~", 10*time.Minute, 1*time.Second))
				ack = &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 2}}}
				err = handler.ReceivedAck(ack, 2, protocol.Encryption1RTT, time.Now())
				Expect(err).NotTo(HaveOccurred())
				Expect(handler.rttStats.LatestRTT()).To(BeNumerically("~", 5*time.Minute, 1*time.Second))
				ack = &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 6}}}
				err = handler.ReceivedAck(ack, 3, protocol.Encryption1RTT, time.Now())
				Expect(err).NotTo(HaveOccurred())
				Expect(handler.rttStats.LatestRTT()).To(BeNumerically("~", 1*time.Minute, 1*time.Second))
			})

			It("uses the DelayTime in the ACK frame", func() {
				now := time.Now()
				// make sure the rttStats have a min RTT, so that the delay is used
				handler.rttStats.UpdateRTT(5*time.Minute, 0, time.Now())
				getPacket(1, protocol.Encryption1RTT).SendTime = now.Add(-10 * time.Minute)
				ack := &wire.AckFrame{
					AckRanges: []wire.AckRange{{Smallest: 1, Largest: 1}},
					DelayTime: 5 * time.Minute,
				}
				err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
				Expect(err).NotTo(HaveOccurred())
				Expect(handler.rttStats.LatestRTT()).To(BeNumerically("~", 5*time.Minute, 1*time.Second))
			})
		})

		Context("determining which ACKs we have received an ACK for", func() {
			BeforeEach(func() {
				ack1 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 80, Largest: 100}}}
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 50, Largest: 200}}}
				morePackets := []*Packet{
					{PacketNumber: 13, Frames: []wire.Frame{ack1, &streamFrame}, Length: 1, EncryptionLevel: protocol.Encryption1RTT},
					{PacketNumber: 14, Frames: []wire.Frame{ack2, &streamFrame}, Length: 1, EncryptionLevel: protocol.Encryption1RTT},
					{PacketNumber: 15, Frames: []wire.Frame{&streamFrame}, Length: 1, EncryptionLevel: protocol.Encryption1RTT},
				}
				for _, packet := range morePackets {
					handler.SentPacket(packet)
				}
			})

			It("determines which ACK we have received an ACK for", func() {
				ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 13, Largest: 15}}}
				Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.GetLowestPacketNotConfirmedAcked()).To(Equal(protocol.PacketNumber(201)))
			})

			It("doesn't do anything when the acked packet didn't contain an ACK", func() {
				ack1 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 13, Largest: 13}}}
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 15, Largest: 15}}}
				Expect(handler.ReceivedAck(ack1, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.GetLowestPacketNotConfirmedAcked()).To(Equal(protocol.PacketNumber(101)))
				Expect(handler.ReceivedAck(ack2, 2, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.GetLowestPacketNotConfirmedAcked()).To(Equal(protocol.PacketNumber(101)))
			})

			It("doesn't decrease the value", func() {
				ack1 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 14, Largest: 14}}}
				ack2 := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 13, Largest: 13}}}
				Expect(handler.ReceivedAck(ack1, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.GetLowestPacketNotConfirmedAcked()).To(Equal(protocol.PacketNumber(201)))
				Expect(handler.ReceivedAck(ack2, 2, protocol.Encryption1RTT, time.Now())).To(Succeed())
				Expect(handler.GetLowestPacketNotConfirmedAcked()).To(Equal(protocol.PacketNumber(201)))
			})
		})
	})

	Context("ACK processing, for retransmitted packets", func() {
		It("sends a packet as retransmission", func() {
			// packet 5 was retransmitted as packet 6
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, Length: 10, EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(10)))
			losePacket(5, protocol.Encryption1RTT)
			Expect(handler.bytesInFlight).To(BeZero())
			handler.SentPacketsAsRetransmission([]*Packet{retransmittablePacket(&Packet{PacketNumber: 6, Length: 11})}, 5)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(11)))
		})

		It("removes a packet when it is acked", func() {
			// packet 5 was retransmitted as packet 6
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, Length: 10}))
			losePacket(5, protocol.Encryption1RTT)
			handler.SentPacketsAsRetransmission([]*Packet{retransmittablePacket(&Packet{PacketNumber: 6, Length: 11})}, 5)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(11)))
			// ack 5
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 5, Largest: 5}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
			expectInPacketHistory([]protocol.PacketNumber{6}, protocol.Encryption1RTT)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(11)))
		})

		It("handles ACKs that ack the original packet as well as the retransmission", func() {
			// packet 5 was retransmitted as packet 7
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, Length: 10}))
			losePacket(5, protocol.Encryption1RTT)
			handler.SentPacketsAsRetransmission([]*Packet{retransmittablePacket(&Packet{PacketNumber: 7, Length: 11})}, 5)
			// ack 5 and 7
			ack := &wire.AckFrame{
				AckRanges: []wire.AckRange{
					{Smallest: 7, Largest: 7},
					{Smallest: 5, Largest: 5},
				},
			}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(handler.oneRTTPackets.history.Len()).To(BeZero())
			Expect(handler.bytesInFlight).To(BeZero())
		})
	})

	It("does not dequeue a packet if no ACK has been received", func() {
		handler.SentPacket(&Packet{PacketNumber: 1, EncryptionLevel: protocol.Encryption1RTT, SendTime: time.Now().Add(-time.Hour)})
		Expect(handler.DequeuePacketForRetransmission()).To(BeNil())
	})

	Context("congestion", func() {
		var cong *mocks.MockSendAlgorithm

		BeforeEach(func() {
			cong = mocks.NewMockSendAlgorithm(mockCtrl)
			handler.congestion = cong
		})

		It("should call OnSent", func() {
			cong.EXPECT().OnPacketSent(
				gomock.Any(),
				protocol.ByteCount(42),
				protocol.PacketNumber(1),
				protocol.ByteCount(42),
				true,
			)
			cong.EXPECT().TimeUntilSend(gomock.Any())
			handler.SentPacket(&Packet{
				PacketNumber:    1,
				Length:          42,
				Frames:          []wire.Frame{&wire.PingFrame{}},
				EncryptionLevel: protocol.Encryption1RTT,
			})
		})

		It("should call MaybeExitSlowStart and OnPacketAcked", func() {
			rcvTime := time.Now().Add(-5 * time.Second)
			cong.EXPECT().OnPacketSent(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(3)
			cong.EXPECT().TimeUntilSend(gomock.Any()).Times(3)
			gomock.InOrder(
				cong.EXPECT().MaybeExitSlowStart(), // must be called before packets are acked
				cong.EXPECT().OnPacketAcked(protocol.PacketNumber(1), protocol.ByteCount(1), protocol.ByteCount(3), rcvTime),
				cong.EXPECT().OnPacketAcked(protocol.PacketNumber(2), protocol.ByteCount(1), protocol.ByteCount(3), rcvTime),
			)
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3}))
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 2}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, rcvTime)
			Expect(err).NotTo(HaveOccurred())
		})

		It("doesn't call OnPacketAcked when a retransmitted packet is acked", func() {
			cong.EXPECT().OnPacketSent(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(2)
			cong.EXPECT().TimeUntilSend(gomock.Any()).Times(2)
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2}))
			// lose packet 1
			gomock.InOrder(
				cong.EXPECT().MaybeExitSlowStart(),
				cong.EXPECT().OnPacketAcked(protocol.PacketNumber(2), protocol.ByteCount(1), protocol.ByteCount(2), gomock.Any()),
				cong.EXPECT().OnPacketLost(protocol.PacketNumber(1), protocol.ByteCount(1), protocol.ByteCount(2)),
			)
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 2, Largest: 2}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
			// don't EXPECT any further calls to the congestion controller
			ack = &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 2}}}
			err = handler.ReceivedAck(ack, 2, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
		})

		It("calls OnPacketAcked and OnPacketLost with the right bytes_in_flight value", func() {
			cong.EXPECT().OnPacketSent(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(4)
			cong.EXPECT().TimeUntilSend(gomock.Any()).Times(4)
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: time.Now().Add(-30 * time.Minute)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3, SendTime: time.Now().Add(-30 * time.Minute)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 4, SendTime: time.Now()}))
			// receive the first ACK
			gomock.InOrder(
				cong.EXPECT().MaybeExitSlowStart(),
				cong.EXPECT().OnPacketAcked(protocol.PacketNumber(2), protocol.ByteCount(1), protocol.ByteCount(4), gomock.Any()),
				cong.EXPECT().OnPacketLost(protocol.PacketNumber(1), protocol.ByteCount(1), protocol.ByteCount(4)),
			)
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 2, Largest: 2}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now().Add(-30*time.Minute))
			Expect(err).ToNot(HaveOccurred())
			// receive the second ACK
			gomock.InOrder(
				cong.EXPECT().MaybeExitSlowStart(),
				cong.EXPECT().OnPacketAcked(protocol.PacketNumber(4), protocol.ByteCount(1), protocol.ByteCount(2), gomock.Any()),
				cong.EXPECT().OnPacketLost(protocol.PacketNumber(3), protocol.ByteCount(1), protocol.ByteCount(2)),
			)
			ack = &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 4, Largest: 4}}}
			err = handler.ReceivedAck(ack, 2, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
		})

		It("only allows sending of ACKs when congestion limited", func() {
			handler.bytesInFlight = 100
			cong.EXPECT().GetCongestionWindow().Return(protocol.ByteCount(200))
			Expect(handler.SendMode()).To(Equal(SendAny))
			cong.EXPECT().GetCongestionWindow().Return(protocol.ByteCount(75))
			Expect(handler.SendMode()).To(Equal(SendAck))
		})

		It("only allows sending of ACKs when we're keeping track of MaxOutstandingSentPackets packets", func() {
			cong.EXPECT().GetCongestionWindow().Return(protocol.MaxByteCount).AnyTimes()
			cong.EXPECT().TimeUntilSend(gomock.Any()).AnyTimes()
			cong.EXPECT().OnPacketSent(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			for i := protocol.PacketNumber(1); i < protocol.MaxOutstandingSentPackets; i++ {
				handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: i}))
				Expect(handler.SendMode()).To(Equal(SendAny))
			}
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: protocol.MaxOutstandingSentPackets}))
			Expect(handler.SendMode()).To(Equal(SendAck))
		})

		It("doesn't allow retransmission if congestion limited", func() {
			handler.bytesInFlight = 100
			handler.retransmissionQueue = []*Packet{{PacketNumber: 3}}
			cong.EXPECT().GetCongestionWindow().Return(protocol.ByteCount(50))
			Expect(handler.SendMode()).To(Equal(SendAck))
		})

		It("allows sending retransmissions", func() {
			cong.EXPECT().GetCongestionWindow().Return(protocol.MaxByteCount)
			handler.retransmissionQueue = []*Packet{{PacketNumber: 3}}
			Expect(handler.SendMode()).To(Equal(SendRetransmission))
		})

		It("allow retransmissions, if we're keeping track of between MaxOutstandingSentPackets and MaxTrackedSentPackets packets", func() {
			cong.EXPECT().GetCongestionWindow().Return(protocol.MaxByteCount)
			Expect(protocol.MaxOutstandingSentPackets).To(BeNumerically("<", protocol.MaxTrackedSentPackets))
			handler.retransmissionQueue = make([]*Packet, protocol.MaxOutstandingSentPackets+10)
			Expect(handler.SendMode()).To(Equal(SendRetransmission))
			handler.retransmissionQueue = make([]*Packet, protocol.MaxTrackedSentPackets)
			Expect(handler.SendMode()).To(Equal(SendNone))
		})

		It("allows RTOs, even when congestion limited", func() {
			// note that we don't EXPECT a call to GetCongestionWindow
			// that means retransmissions are sent without considering the congestion window
			handler.numProbesToSend = 1
			handler.retransmissionQueue = []*Packet{{PacketNumber: 3}}
			Expect(handler.SendMode()).To(Equal(SendPTO))
		})

		It("gets the pacing delay", func() {
			sendTime := time.Now().Add(-time.Minute)
			handler.bytesInFlight = 100
			cong.EXPECT().OnPacketSent(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any())
			cong.EXPECT().TimeUntilSend(protocol.ByteCount(100)).Return(time.Hour)
			handler.SentPacket(&Packet{PacketNumber: 1, SendTime: sendTime, EncryptionLevel: protocol.Encryption1RTT})
			Expect(handler.TimeUntilSend()).To(Equal(sendTime.Add(time.Hour)))
		})

		It("allows sending of all RTO probe packets", func() {
			handler.numProbesToSend = 5
			Expect(handler.ShouldSendNumPackets()).To(Equal(5))
		})

		It("allows sending of one packet, if it should be sent immediately", func() {
			cong.EXPECT().TimeUntilSend(gomock.Any()).Return(time.Duration(0))
			Expect(handler.ShouldSendNumPackets()).To(Equal(1))
		})

		It("allows sending of multiple packets, if the pacing delay is smaller than the minimum", func() {
			pacingDelay := protocol.MinPacingDelay / 10
			cong.EXPECT().TimeUntilSend(gomock.Any()).Return(pacingDelay)
			Expect(handler.ShouldSendNumPackets()).To(Equal(10))
		})

		It("allows sending of multiple packets, if the pacing delay is smaller than the minimum, and not a fraction", func() {
			pacingDelay := protocol.MinPacingDelay * 2 / 5
			cong.EXPECT().TimeUntilSend(gomock.Any()).Return(pacingDelay)
			Expect(handler.ShouldSendNumPackets()).To(Equal(3))
		})
	})

	It("doesn't set an alarm if there are no outstanding packets", func() {
		handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 10}))
		handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 11}))
		ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 10, Largest: 11}}}
		err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
		Expect(err).ToNot(HaveOccurred())
		Expect(handler.GetAlarmTimeout()).To(BeZero())
	})

	It("does nothing on OnAlarm if there are no outstanding packets", func() {
		Expect(handler.OnAlarm()).To(Succeed())
		Expect(handler.SendMode()).To(Equal(SendAny))
	})

	Context("probe packets", func() {
		It("uses the RTT from RTT stats", func() {
			rtt := 2 * time.Second
			updateRTT(rtt)
			Expect(handler.rttStats.SmoothedOrInitialRTT()).To(Equal(2 * time.Second))
			Expect(handler.rttStats.MeanDeviation()).To(Equal(time.Second))
			Expect(handler.computePTOTimeout()).To(Equal(time.Duration(2+4) * time.Second))
		})

		It("uses the granularity for short RTTs", func() {
			rtt := time.Microsecond
			updateRTT(rtt)
			Expect(handler.computePTOTimeout()).To(Equal(granularity))
		})

		It("implements exponential backoff", func() {
			handler.ptoCount = 0
			timeout := handler.computePTOTimeout()
			Expect(timeout).ToNot(BeZero())
			handler.ptoCount = 1
			Expect(handler.computePTOTimeout()).To(Equal(2 * timeout))
			handler.ptoCount = 2
			Expect(handler.computePTOTimeout()).To(Equal(4 * timeout))
		})

		It("sets the TPO send mode until two  packets is sent", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.OnAlarm()
			Expect(handler.SendMode()).To(Equal(SendPTO))
			Expect(handler.ShouldSendNumPackets()).To(Equal(2))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2}))
			Expect(handler.SendMode()).To(Equal(SendPTO))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3}))
			Expect(handler.SendMode()).ToNot(Equal(SendPTO))
		})

		It("only counts retransmittable packets as probe packets", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.OnAlarm()
			Expect(handler.SendMode()).To(Equal(SendPTO))
			Expect(handler.ShouldSendNumPackets()).To(Equal(2))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2}))
			Expect(handler.SendMode()).To(Equal(SendPTO))
			for p := protocol.PacketNumber(3); p < 30; p++ {
				handler.SentPacket(nonRetransmittablePacket(&Packet{PacketNumber: p}))
				Expect(handler.SendMode()).To(Equal(SendPTO))
			}
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 30}))
			Expect(handler.SendMode()).ToNot(Equal(SendPTO))
		})

		It("gets two probe packets if RTO expires", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2}))

			updateRTT(time.Hour)
			Expect(handler.lossTime.IsZero()).To(BeTrue())

			handler.OnAlarm() // TLP
			handler.OnAlarm() // TLP
			handler.OnAlarm() // RTO
			p, err := handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			Expect(p).ToNot(BeNil())
			Expect(p.PacketNumber).To(Equal(protocol.PacketNumber(1)))
			p, err = handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			Expect(p).ToNot(BeNil())
			Expect(p.PacketNumber).To(Equal(protocol.PacketNumber(2)))
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(2)))

			Expect(handler.ptoCount).To(BeEquivalentTo(3))
		})

		It("doesn't delete packets transmitted as PTO from the history", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: time.Now().Add(-time.Hour)}))
			handler.rttStats.UpdateRTT(time.Second, 0, time.Now())
			handler.OnAlarm() // TLP
			handler.OnAlarm() // TLP
			handler.OnAlarm() // RTO
			_, err := handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			_, err = handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			expectInPacketHistory([]protocol.PacketNumber{1, 2}, protocol.Encryption1RTT)
			Expect(handler.bytesInFlight).To(Equal(protocol.ByteCount(2)))
			// Send a probe packet and receive an ACK for it.
			// This verifies the RTO.
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3}))
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 3, Largest: 3}}}
			Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())).To(Succeed())
			Expect(err).ToNot(HaveOccurred())
			Expect(handler.oneRTTPackets.history.Len()).To(BeZero())
			Expect(handler.bytesInFlight).To(BeZero())
			Expect(handler.retransmissionQueue).To(BeEmpty()) // 1 and 2 were already sent as probe packets
		})

		It("gets packets sent before the probe packet for retransmission", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 4, SendTime: time.Now().Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, SendTime: time.Now().Add(-time.Hour)}))
			handler.OnAlarm() // TLP
			handler.OnAlarm() // TLP
			handler.OnAlarm() // RTO
			_, err := handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			_, err = handler.DequeueProbePacket()
			Expect(err).ToNot(HaveOccurred())
			expectInPacketHistory([]protocol.PacketNumber{1, 2, 3, 4, 5}, protocol.Encryption1RTT)
			// Send a probe packet and receive an ACK for it.
			// This verifies the RTO.
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 6}))
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 6, Largest: 6}}}
			err = handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(handler.oneRTTPackets.history.Len()).To(BeZero())
			Expect(handler.bytesInFlight).To(BeZero())
			Expect(handler.retransmissionQueue).To(HaveLen(3)) // packets 3, 4, 5
		})

		It("handles ACKs for the original packet", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, SendTime: time.Now().Add(-time.Hour)}))
			handler.rttStats.UpdateRTT(time.Second, 0, time.Now())
			handler.OnAlarm() // TLP
			handler.OnAlarm() // TLP
			handler.OnAlarm() // RTO
			handler.SentPacketsAsRetransmission([]*Packet{retransmittablePacket(&Packet{PacketNumber: 6})}, 5)
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 5, Largest: 5}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, time.Now())
			Expect(err).ToNot(HaveOccurred())
			err = handler.OnAlarm()
			Expect(err).ToNot(HaveOccurred())
		})

		It("handles ACKs for the original packet", func() {
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 5, SendTime: time.Now().Add(-time.Hour)}))
			handler.rttStats.UpdateRTT(time.Second, 0, time.Now())
			err := handler.OnAlarm()
			Expect(err).ToNot(HaveOccurred())
			err = handler.OnAlarm()
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Context("Delay-based loss detection", func() {
		It("immediately detects old packets as lost when receiving an ACK", func() {
			now := time.Now()
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: now.Add(-time.Hour)}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: now.Add(-time.Second)}))
			Expect(handler.lossTime.IsZero()).To(BeTrue())

			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 2, Largest: 2}}}
			err := handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, now)
			Expect(err).NotTo(HaveOccurred())
			Expect(handler.DequeuePacketForRetransmission()).ToNot(BeNil())
			Expect(handler.DequeuePacketForRetransmission()).To(BeNil())
			// no need to set an alarm, since packet 1 was already declared lost
			Expect(handler.lossTime.IsZero()).To(BeTrue())
			Expect(handler.bytesInFlight).To(BeZero())
		})

		It("sets the early retransmit alarm", func() {
			now := time.Now()
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 1, SendTime: now.Add(-2 * time.Second), EncryptionLevel: protocol.Encryption1RTT}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 2, SendTime: now.Add(-2 * time.Second), EncryptionLevel: protocol.Encryption1RTT}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 3, SendTime: now.Add(-time.Second), EncryptionLevel: protocol.Encryption1RTT}))
			Expect(handler.lossTime.IsZero()).To(BeTrue())

			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 2, Largest: 2}}}
			Expect(handler.ReceivedAck(ack, 1, protocol.Encryption1RTT, now.Add(-time.Second))).To(Succeed())
			Expect(handler.rttStats.SmoothedRTT()).To(Equal(time.Second))

			// Packet 1 should be considered lost (1+1/8) RTTs after it was sent.
			Expect(handler.lossTime.IsZero()).To(BeFalse())
			Expect(handler.lossTime.Sub(getPacket(1, protocol.Encryption1RTT).SendTime)).To(Equal(time.Second * 9 / 8))

			Expect(handler.OnAlarm()).To(Succeed())
			Expect(handler.DequeuePacketForRetransmission()).NotTo(BeNil())
			// make sure this is not an RTO: only packet 1 is retransmissted
			Expect(handler.DequeuePacketForRetransmission()).To(BeNil())
		})
	})

	Context("crypto packets", func() {
		BeforeEach(func() {
			handler.handshakeComplete = false
		})

		It("detects the crypto timeout", func() {
			now := time.Now()
			sendTime := now.Add(-time.Minute)
			lastCryptoPacketSendTime := now.Add(-30 * time.Second)
			// send Initial packets: 1, 3
			// send 1-RTT packet: 100
			handler.SentPacket(cryptoPacket(&Packet{PacketNumber: 1, SendTime: sendTime, EncryptionLevel: protocol.EncryptionInitial}))
			handler.SentPacket(cryptoPacket(&Packet{PacketNumber: 3, SendTime: sendTime, EncryptionLevel: protocol.EncryptionInitial}))
			handler.SentPacket(retransmittablePacket(&Packet{PacketNumber: 100, SendTime: sendTime, EncryptionLevel: protocol.Encryption1RTT}))

			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 1, Largest: 1}}}
			Expect(handler.ReceivedAck(ack, 1, protocol.EncryptionInitial, now)).To(Succeed())
			// RTT is now 1 minute
			Expect(handler.rttStats.SmoothedRTT()).To(Equal(time.Minute))
			Expect(handler.lossTime.IsZero()).To(BeTrue())
			Expect(handler.GetAlarmTimeout().Sub(sendTime)).To(Equal(2 * time.Minute))

			Expect(handler.OnAlarm()).To(Succeed())
			p := handler.DequeuePacketForRetransmission()
			Expect(p).ToNot(BeNil())
			Expect(p.PacketNumber).To(Equal(protocol.PacketNumber(3)))
			Expect(handler.cryptoCount).To(BeEquivalentTo(1))
			handler.SentPacket(cryptoPacket(&Packet{PacketNumber: 4, SendTime: lastCryptoPacketSendTime}))
			// make sure the exponential backoff is used
			Expect(handler.GetAlarmTimeout().Sub(lastCryptoPacketSendTime)).To(Equal(4 * time.Minute))
		})

		It("rejects an ACK that acks packets with a higher encryption level", func() {
			handler.SentPacket(&Packet{
				PacketNumber:    13,
				EncryptionLevel: protocol.Encryption1RTT,
				Frames:          []wire.Frame{&streamFrame},
				Length:          1,
			})
			ack := &wire.AckFrame{AckRanges: []wire.AckRange{{Smallest: 13, Largest: 13}}}
			err := handler.ReceivedAck(ack, 1, protocol.EncryptionHandshake, time.Now())
			Expect(err).To(MatchError("PROTOCOL_VIOLATION: Received ACK for an unsent packet"))
		})

		It("deletes crypto packets when the handshake completes", func() {
			for i := protocol.PacketNumber(0); i < 6; i++ {
				p := retransmittablePacket(&Packet{PacketNumber: i, EncryptionLevel: protocol.EncryptionInitial})
				handler.SentPacket(p)
			}
			for i := protocol.PacketNumber(0); i <= 6; i++ {
				p := retransmittablePacket(&Packet{PacketNumber: i, EncryptionLevel: protocol.EncryptionHandshake})
				handler.SentPacket(p)
			}
			handler.queuePacketForRetransmission(getPacket(1, protocol.EncryptionInitial), handler.getPacketNumberSpace(protocol.EncryptionInitial))
			handler.queuePacketForRetransmission(getPacket(3, protocol.EncryptionHandshake), handler.getPacketNumberSpace(protocol.EncryptionHandshake))
			handler.SetHandshakeComplete()
			Expect(handler.initialPackets.history.Len()).To(BeZero())
			Expect(handler.handshakePackets.history.Len()).To(BeZero())
			packet := handler.DequeuePacketForRetransmission()
			Expect(packet).To(BeNil())
		})
	})

	Context("peeking and popping packet number", func() {
		It("peeks and pops the initial packet number", func() {
			pn, _ := handler.PeekPacketNumber(protocol.EncryptionInitial)
			Expect(pn).To(Equal(protocol.PacketNumber(42)))
			Expect(handler.PopPacketNumber(protocol.EncryptionInitial)).To(Equal(protocol.PacketNumber(42)))
		})

		It("peeks and pops beyond the initial packet number", func() {
			Expect(handler.PopPacketNumber(protocol.EncryptionInitial)).To(Equal(protocol.PacketNumber(42)))
			Expect(handler.PopPacketNumber(protocol.EncryptionInitial)).To(BeNumerically(">", 42))
		})

		It("starts at 0 for handshake and application-data packet number space", func() {
			pn, _ := handler.PeekPacketNumber(protocol.EncryptionHandshake)
			Expect(pn).To(BeZero())
			Expect(handler.PopPacketNumber(protocol.EncryptionHandshake)).To(BeZero())
			pn, _ = handler.PeekPacketNumber(protocol.Encryption1RTT)
			Expect(pn).To(BeZero())
			Expect(handler.PopPacketNumber(protocol.Encryption1RTT)).To(BeZero())
		})
	})

	Context("resetting for retry", func() {
		It("queues outstanding packets for retransmission and cancels alarms", func() {
			packet := &Packet{
				PacketNumber:    42,
				EncryptionLevel: protocol.EncryptionInitial,
				Frames:          []wire.Frame{&wire.CryptoFrame{Data: []byte("foobar")}},
				Length:          100,
			}
			handler.SentPacket(packet)
			Expect(handler.GetAlarmTimeout()).ToNot(BeZero())
			Expect(handler.bytesInFlight).ToNot(BeZero())
			Expect(handler.DequeuePacketForRetransmission()).To(BeNil())
			Expect(handler.SendMode()).To(Equal(SendAny))
			// now receive a Retry
			Expect(handler.ResetForRetry()).To(Succeed())
			Expect(handler.bytesInFlight).To(BeZero())
			Expect(handler.GetAlarmTimeout()).To(BeZero())
			Expect(handler.SendMode()).To(Equal(SendRetransmission))
			p := handler.DequeuePacketForRetransmission()
			Expect(p.PacketNumber).To(Equal(packet.PacketNumber))
			Expect(p.Frames).To(Equal(packet.Frames))
		})
	})
})
