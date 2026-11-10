package client_test

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// The examples need a running ined-server, so they are compiled but not
// run by go test.

func Example() {
	c, err := client.New(client.Options{URL: "http://127.0.0.1:8470", Token: "secret"})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	net, err := c.CreateGroup(ctx, apitypes.GroupCreate{CIDR: "192.168.1.0/24", Name: "office"})
	if err != nil {
		log.Fatal(err)
	}
	// Without a parent the host goes into the matching network group.
	h, err := c.CreateHost(ctx, apitypes.HostCreate{IP: "192.168.1.10", Name: "printer"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(h.DisplayName, *h.ParentID == net.ID)
}

func ExampleAPIError() {
	c, _ := client.New(client.Options{URL: "http://127.0.0.1:8470"})
	_, err := c.Host(context.Background(), 42)
	var ae *client.APIError
	switch {
	case errors.As(err, &ae) && ae.Code == apitypes.CodeNotFound:
		fmt.Println("no host 42")
	case err != nil:
		log.Fatal(err) // connection problem or another API error
	}
}

func ExampleClient_UpdateHost() {
	c, _ := client.New(client.Options{URL: "http://127.0.0.1:8470"})
	// Only fields that are set change; a nil parent moves the host to the root.
	_, err := c.UpdateHost(context.Background(), 7, apitypes.HostPatch{
		Note:     apitypes.Some("replaced 2026-11"),
		ParentID: apitypes.Some[*int64](nil),
	})
	if err != nil {
		log.Fatal(err)
	}
}

func ExampleClient_Events() {
	c, _ := client.New(client.Options{URL: "http://127.0.0.1:8470"})
	ctx := context.Background()
	reload := func() {
		m, err := c.Map(ctx)
		if err == nil {
			fmt.Println(len(m.Items), "items")
		}
	}
	reload()
	st := c.Events(ctx, client.StreamOptions{
		OnState: func(s client.StreamState) {
			if !s.Connected {
				log.Printf("event stream lost (%v); retrying in %s", s.Err, s.RetryIn)
			}
		},
	})
	defer st.Close()
	for ev := range st.C() {
		switch ev.Type {
		case apitypes.EventResync:
			reload() // missed events: load the full state again
		case apitypes.EventHostStatus:
			var h apitypes.Host
			if ev.Decode(&h) == nil {
				fmt.Println(h.IP, h.Status)
			}
		}
	}
	if err := st.Err(); err != nil {
		log.Fatal(err) // e.g. 401: wrong token
	}
}

func ExampleClient_StartDiscovery() {
	c, _ := client.New(client.Options{URL: "http://127.0.0.1:8470"})
	ctx := context.Background()
	// Subscribe first, so the job's events can't be missed.
	st := c.Events(ctx, client.StreamOptions{})
	defer st.Close()
	<-st.Ready()
	job, err := c.StartDiscovery(ctx, apitypes.DiscoveryCreate{CIDR: "192.168.1.0/24"})
	if err != nil {
		log.Fatal(err)
	}
	for ev := range st.C() {
		var d apitypes.Discovery
		if ev.Type == apitypes.EventDiscoveryFinished && ev.Decode(&d) == nil && d.ID == job.ID {
			fmt.Println(client.DiscoveryLine(d))
			return
		}
	}
}
