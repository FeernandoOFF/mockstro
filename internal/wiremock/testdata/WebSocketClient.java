import java.net.URI;
import java.net.http.*;
import java.util.concurrent.*;

class WebSocketClient {
 public static void main(String[] args) throws Exception {
  BlockingQueue<String> messages=new LinkedBlockingQueue<>();
  WebSocket socket=HttpClient.newHttpClient().newWebSocketBuilder().buildAsync(URI.create(args[0]),new WebSocket.Listener(){
   StringBuilder partial=new StringBuilder();
   public void onOpen(WebSocket ws){ws.request(1);}
   public CompletionStage<?> onText(WebSocket ws,CharSequence text,boolean last){partial.append(text);if(last){messages.add(partial.toString());partial.setLength(0);}ws.request(1);return null;}
  }).get(5,TimeUnit.SECONDS);
  socket.sendText("{\"type\":\"req\",\"id\":\"42\",\"method\":\"connect\"}",true).join();
  String response=messages.poll(5,TimeUnit.SECONDS);
  if(response==null||!response.contains("\"id\":\"42\""))throw new Exception("Bad response: "+response);
  System.out.println("CONNECTED");System.out.flush();
  String event=messages.poll(10,TimeUnit.SECONDS);
  if(!"pushed-event".equals(event))throw new Exception("Bad push: "+event);
  socket.sendClose(WebSocket.NORMAL_CLOSURE,"done").join();
 }
}
