import axios from 'axios';

export class ChatRestClient {
  constructor(baseUrl, token) {
    this.baseUrl = baseUrl;
    this.token = token;
    this.client = axios.create({
      baseURL: baseUrl,
      headers: {
        'Authorization': `Bearer ${token}`,
        'Content-Type': 'application/json'
      }
    });
  }

  async sendMessage(channelId, content) {
    try {
      const response = await this.client.post(`/api/messages/${channelId}`, {
        content
      });
      return response.data;
    } catch (error) {
      throw new Error(`Failed to send message: ${error.response?.data || error.message}`);
    }
  }

  async getMessages(channelId) {
    try {
      const response = await this.client.get(`/api/messages/${channelId}`);
      return response.data;
    } catch (error) {
      throw new Error(`Failed to get messages: ${error.response?.data || error.message}`);
    }
  }

  async getClientCounts(channelIds) {
    try {
      const response = await axios.get(`${this.baseUrl}/api/messages/counts`, {
        data: { channels: channelIds }
      });
      return response.data;
    } catch (error) {
      throw new Error(`Failed to get client counts: ${error.response?.data || error.message}`);
    }
  }

  async health() {
    try {
      const response = await axios.get(`${this.baseUrl}/health`);
      return response.data;
    } catch (error) {
      throw new Error(`Health check failed: ${error.message}`);
    }
  }
}
