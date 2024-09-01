# hls

An HLS server which uses an Icecast origin server to relay ADTS/AAC streams.

Aruments are the port to listen on for clients, the origin server base URL and then a list of mountpoint names, eg.:

`go run hls.go :8888 http://origin.example.com Blues Soul Rock Classical`

You can also specify some flags:

* -r <url> to provide a redirect URL for paths which don't match stream patterns
* -m <number> the minimum number of active streams to consider the server as healthy (for load balancers - /healthy)
