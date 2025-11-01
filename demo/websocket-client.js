import WebSocket from 'ws';

export class ChatWebSocketClient {
  constructor(serverUrl, token, channelId) {
    this.serverUrl = serverUrl;
    this.token = token;
    this.channelId = channelId;
    this.ws = null;
    this.messageHandlers = [];
  }

  connect() {
    return new Promise((resolve, reject) => {
      const url = `${this.serverUrl}/ws?channel=${this.channelId}`;
      this.ws = new WebSocket(url, {
        headers: {
          'Authorization': `Bearer ${this.token}`
        }
      });

      this.ws.on('open', () => {
        console.log(`✓ Connected to channel: ${this.channelId}`);
        resolve();
      });

      this.ws.on('message', (data) => {
        try {
          const message = JSON.parse(data.toString());
          this.messageHandlers.forEach(handler => handler(message));
        } catch (error) {
          console.error('Failed to parse message:', error.message);
        }
      });

      this.ws.on('error', (error) => {
        console.error('WebSocket error:', error.message);
        reject(error);
      });

      this.ws.on('close', () => {
        console.log('✗ Disconnected from server');
      });
    });
  }

  onMessage(handler) {
    this.messageHandlers.push(handler);
  }

  disconnect() {
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }
}
