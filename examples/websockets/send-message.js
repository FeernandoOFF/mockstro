var base = 'http://127.0.0.1:' + WIREMOCK_PORT;
var channels = http.get(base + '/__admin/channels');
if (channels.status !== 200) throw new Error('Cannot read WireMock channels: ' + channels.body);
var active = json(channels.body).channels.some(function (channel) {
    return channel.open && channel.type === 'websocket' && channel.initiatingRequest.url === WS_PATH;
});
if (!active) throw new Error('No active WebSocket channel at ' + WS_PATH);
var response = http.post(base + '/__admin/channels/send', {
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
        type: 'websocket',
        initiatingRequest: { url: WS_PATH },
        message: { body: { data: WS_MESSAGE } }
    })
});
if (response.status !== 200) throw new Error('WireMock send failed: ' + response.body);
