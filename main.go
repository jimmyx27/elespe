package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type Message struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Verse   string `json:"verse,omitempty"`
	Number  int    `json:"number,omitempty"`
	Total   int    `json:total,omitempty"`
}
