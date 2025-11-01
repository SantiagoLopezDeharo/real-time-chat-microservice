import WebSocket from 'ws';

export class ChatWebSocketClient {
  constructor(serverUrl, token) {
    this.serverUrl = serverUrl;
    this.token = token;
    this.ws = null;
    this.messageHandler = null;
  }

  connect() {
    return new Promise((resolve, reject) => {
      // WebSocket connects to user's personal channel
      const url = `${this.serverUrl}/ws`;
      
      this.ws = new WebSocket(url, {
        headers: {
          'Authorization': `Bearer ${this.token}`
        }
      });

      this.ws.on('open', () => {
        console.log('✓ Connected to WebSocket');
        resolve();
      });

      this.ws.on('message', (data) => {
        try {
          const message = JSON.parse(data.toString());
          if (this.messageHandler) {
            this.messageHandler(message);
          }
        } catch (error) {
          console.error('Failed to parse message:', error);
        }
      });

      this.ws.on('error', (error) => {
        console.error('WebSocket error:', error.message);
        reject(error);
      });

      this.ws.on('close', () => {
        console.log('✗ Disconnected from WebSocket');
      });
    });
  }

  onMessage(handler) {
    this.messageHandler = handler;
  }

  disconnect() {
    if (this.ws) {
      this.ws.close();
    }
  }
}
