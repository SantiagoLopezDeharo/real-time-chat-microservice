import axios from 'axios';

export class ChatRestClient {
  constructor(serverUrl, token) {
    this.serverUrl = serverUrl;
    this.token = token;
  }

  async sendMessage(participants, content) {
    try {
      const response = await axios.post(
        `${this.serverUrl}/api/messages`,
        {
          participants: participants,
          content: content
        },
        {
          headers: {
            'Authorization': `Bearer ${this.token}`,
            'Content-Type': 'application/json'
          }
        }
      );
      return response.data;
    } catch (error) {
      throw new Error(error.response?.data || error.message);
    }
  }

  async getMessages(participants, page = 0, size = 50) {
    try {
      const participantsQuery = participants.join(',');
      const response = await axios.get(
        `${this.serverUrl}/api/messages/get?participants=${participantsQuery}&page=${page}&size=${size}`,
        {
          headers: {
            'Authorization': `Bearer ${this.token}`
          }
        }
      );
      return response.data || [];
    } catch (error) {
      throw new Error(error.response?.data || error.message);
    }
  }

  async getUserConnections(users) {
    try {
      const response = await axios.post(
        `${this.serverUrl}/api/connections`,
        {
          users: users
        },
        {
          headers: {
            'Content-Type': 'application/json'
          }
        }
      );
      return response.data;
    } catch (error) {
      throw new Error(error.response?.data || error.message);
    }
  }

  async health() {
    try {
      const response = await axios.get(`${this.serverUrl}/health`);
      return response.data;
    } catch (error) {
      throw new Error(error.response?.data || error.message);
    }
  }
}
