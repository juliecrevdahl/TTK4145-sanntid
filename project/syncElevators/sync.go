package syncElevators

import (
	"fmt"
	"strconv"
	"time"

	. "github.com/perkjelsvik/TTK4145-sanntid/project/config"
	"github.com/perkjelsvik/TTK4145-sanntid/project/networkCommunication/network/peers"
)

// SyncChannels contains all channels between governor - sync and sync - network
type SyncChannels struct {
	UpdateGovernor  chan [NumElevators]Elev
	UpdateSync      chan Elev
	OrderUpdate     chan Keypress
	OnlineElevators chan [NumElevators]bool
	IncomingMsg     chan Message
	OutgoingMsg     chan Message
	PeerUpdate      chan peers.PeerUpdate
	PeerTxEnable    chan bool
}

// How orders up and down are acknowledged between the elevators
/*------------------------------------------------------------------*
|									▲															▼										|
|		{assignedID elev1 elev2 elev3} {assignedID elev1 elev2 elev3}		|
|		{assignedID elev1 elev2 elev3} {assignedID elev1 elev2 elev3}		|
|		{assignedID elev1 elev2 elev3} {assignedID elev1 elev2 elev3}		|
|		{assignedID elev1 elev2 elev3} {assignedID elev1 elev2 elev3}		|
*-------------------------------------------------------------------*/

// Synchronise called as goroutine; forwards data to network, synchronises data from network.
func Synchronise(ch SyncChannels, id int) {
	var (
		registeredOrders [NumFloors][NumButtons - 1]AckList
		elevList         [NumElevators]Elev
		sendMsg          Message
		onlineList       [NumElevators]bool
		recentlyDied     [NumElevators]bool
		someUpdate       bool
		offline          bool
	)

	timeout := make(chan bool)
	go func() { time.Sleep(1 * time.Second); timeout <- true }()

	select {
	case initMsg := <-ch.IncomingMsg:
		elevList = initMsg.Elevator
		registeredOrders = initMsg.RegisteredOrders
		someUpdate = true
	case <-timeout:
		offline = true
	}

	lostID := -1
	reassignTimer := time.NewTimer(5 * time.Second)
	broadcastTicker := time.NewTicker(100 * time.Millisecond)
	singleModeTicker := time.NewTicker(100 * time.Millisecond)
	reassignTimer.Stop()
	singleModeTicker.Stop()

	for {

		if offline {
			if onlineList[id] {
				offline = false
				reInitTimer := time.NewTimer(1 * time.Second)
			REINIT:
				for {
					select {
					case reInitMsg := <-ch.IncomingMsg:
						if reInitMsg.Elevator != elevList && reInitMsg.ID != id {
							tmpElevator := elevList[id]
							elevList = reInitMsg.Elevator
							elevList[id] = tmpElevator
							someUpdate = true
							reInitTimer.Stop()
							break REINIT
						}
					case <-reInitTimer.C:
						break REINIT
					}
				}
			}
		}

		if lostID != -1 {
			fmt.Println("ELEVATOR", lostID, "DIED")
			recentlyDied[lostID] = true
			lostID = -1
		}

		select {
		case newElev := <-ch.UpdateSync:
			oldQueue := elevList[id].Queue
			if newElev.State == Undefined {
				ch.PeerTxEnable <- false
			} else if newElev.State != Undefined && elevList[id].State == Undefined {
				ch.PeerTxEnable <- true
			}

			elevList[id] = newElev
			elevList[id].Queue = oldQueue
			someUpdate = true

		case newOrder := <-ch.OrderUpdate:
			if newOrder.Done {
				elevList[id].Queue[newOrder.Floor] = [NumButtons]bool{}
				someUpdate = true
				if newOrder.Btn != BtnInside {
					registeredOrders[newOrder.Floor][BtnUp].ImplicitAcks[id] = Finished
					registeredOrders[newOrder.Floor][BtnDown].ImplicitAcks[id] = Finished
					fmt.Println("We Finished order", newOrder.Btn, "at floor", newOrder.Floor+1)
				}
			} else {
				if newOrder.Btn == BtnInside {
					elevList[id].Queue[newOrder.Floor][newOrder.Btn] = true
					someUpdate = true
				} else {
					registeredOrders[newOrder.Floor][newOrder.Btn].DesignatedElevator = newOrder.DesignatedElevator
					registeredOrders[newOrder.Floor][newOrder.Btn].ImplicitAcks[id] = Acked
					fmt.Println("We acknowledged a new order", newOrder.Btn, "at floor", newOrder.Floor+1)
					fmt.Println("\tdesignated to", registeredOrders[newOrder.Floor][newOrder.Btn].DesignatedElevator)
				}
			}

		case msg := <-ch.IncomingMsg:
			if msg.ID == id || !onlineList[msg.ID] || !onlineList[id] {
				continue
			} else {
				if msg.Elevator != elevList {
					tmpElevator := elevList[id]
					elevList = msg.Elevator
					elevList[id] = tmpElevator
					someUpdate = true
				}
				for elevator := 0; elevator < NumElevators; elevator++ {
					if elevator == id || !onlineList[msg.ID] || !onlineList[id] {
						continue
					}
					for floor := 0; floor < NumFloors; floor++ {
						for btn := BtnUp; btn < BtnInside; btn++ {
							switch msg.RegisteredOrders[floor][btn].ImplicitAcks[elevator] {
							case NotAcked:
								if registeredOrders[floor][btn].ImplicitAcks[id] == Finished {
									registeredOrders = copyAckList(msg, registeredOrders, elevator, floor, id, btn)
								} else if registeredOrders[floor][btn].ImplicitAcks[elevator] != NotAcked {
									registeredOrders[floor][btn].ImplicitAcks[elevator] = NotAcked
								}

							case Acked:
								if registeredOrders[floor][btn].ImplicitAcks[id] == NotAcked {
									fmt.Println("Order ", btn, "from ", msg.ID, "in floor", floor+1, "has been acked!")
									registeredOrders = copyAckList(msg, registeredOrders, elevator, floor, id, btn)
								} else if registeredOrders[floor][btn].ImplicitAcks[elevator] != Acked {
									registeredOrders[floor][btn].ImplicitAcks[elevator] = Acked
								}
								if checkAllAckStatus(onlineList, registeredOrders[floor][btn].ImplicitAcks, Acked) &&
									!elevList[id].Queue[floor][btn] &&
									registeredOrders[floor][btn].DesignatedElevator == id {
									fmt.Println("We've been assigned a new order!")
									elevList[id].Queue[floor][btn] = true
									someUpdate = true
								}

							case Finished:
								if registeredOrders[floor][btn].ImplicitAcks[id] == Acked {
									registeredOrders = copyAckList(msg, registeredOrders, elevator, floor, id, btn)
								} else if registeredOrders[floor][btn].ImplicitAcks[elevator] != Finished {
									registeredOrders[floor][btn].ImplicitAcks[elevator] = Finished
								}

								if checkAllAckStatus(onlineList, registeredOrders[floor][btn].ImplicitAcks, Finished) {
									registeredOrders[floor][btn].ImplicitAcks[id] = NotAcked
									if registeredOrders[floor][btn].DesignatedElevator == id {
										elevList[id].Queue[floor][btn] = false
										someUpdate = true
									}
								}
							}
						}
					}
				}
				if someUpdate {
					ch.UpdateGovernor <- elevList
					someUpdate = false
				}
			}

		case <-singleModeTicker.C:
			for floor := 0; floor < NumFloors; floor++ {
				for btn := BtnUp; btn < BtnInside; btn++ {
					if registeredOrders[floor][btn].ImplicitAcks[id] == Acked &&
						!elevList[id].Queue[floor][btn] {
						fmt.Println("We've been assigned a new order!")
						elevList[id].Queue[floor][btn] = true
						someUpdate = true
					}
					if registeredOrders[floor][btn].ImplicitAcks[id] == Finished {
						registeredOrders[floor][btn].ImplicitAcks[id] = NotAcked
					}

				}
			}
			if someUpdate {
				ch.UpdateGovernor <- elevList
				someUpdate = false
			}

		case <-broadcastTicker.C:
			if !offline {
				sendMsg.RegisteredOrders = registeredOrders
				sendMsg.Elevator = elevList
				sendMsg.ID = id
				ch.OutgoingMsg <- sendMsg
			}

		case p := <-ch.PeerUpdate:
			fmt.Printf("Peer update:\n")
			fmt.Printf("  Peers:    %q\n", p.Peers)
			fmt.Printf("  New:      %q\n", p.New)
			fmt.Printf("  Lost:     %q\n", p.Lost)
			if len(p.Peers) == 0 {
				offline = true
				singleModeTicker.Stop()
			} else if len(p.Peers) == 1 {
				singleModeTicker = time.NewTicker(100 * time.Millisecond)
			} else {
				singleModeTicker.Stop()
			}

			if len(p.New) > 0 {
				newID, _ := strconv.Atoi(p.New)
				onlineList[newID] = true
			} else if len(p.Lost) > 0 {
				lostID, _ = strconv.Atoi(p.Lost[0])
				onlineList[lostID] = false
				if elevList[lostID].Queue != [NumFloors][NumButtons]bool{} && !recentlyDied[lostID] {
					reassignTimer.Reset(1 * time.Second)
				}
			}
			fmt.Println("Online elevators changed: ", onlineList)
			tmpList := onlineList
			go func() { ch.OnlineElevators <- tmpList }()

		case <-reassignTimer.C:
			for elevator := 0; elevator < NumElevators; elevator++ {
				if !recentlyDied[elevator] {
					continue
				}
				recentlyDied[elevator] = false
				for floor := 0; floor < NumFloors; floor++ {
					for btn := BtnUp; btn < BtnInside; btn++ {
						if elevList[elevator].Queue[floor][btn] {
							elevList[id].Queue[floor][btn] = true
							elevList[elevator].Queue[floor][btn] = false
						}
					}
				}
			}
			ch.UpdateGovernor <- elevList
			someUpdate = false
		}
	}
}
