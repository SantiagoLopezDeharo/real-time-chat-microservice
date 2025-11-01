import readline from 'readline';
import { createToken } from './jwt.js';
import { ChatWebSocketClient } from './websocket-client.js';
import { ChatRestClient } from './rest-client.js';

const SERVER_HTTP = process.env.SERVER_HTTP || 'http://localhost:8080';
const SERVER_WS = process.env.SERVER_WS || 'ws://localhost:8080';

const rl = readline.createInterface({
  input: process.stdin,
  output: process.stdout
});

function question(prompt) {
  return new Promise((resolve) => {
    rl.question(prompt, resolve);
  });
}

function displayBanner() {
  console.log('\n╔═══════════════════════════════════════╗');
  console.log('║   Real-Time Chat Demo Client         ║');
  console.log('╚═══════════════════════════════════════╝\n');
}

function displayMenu() {
  console.log('\n--- Main Menu ---');
  console.log('1. Connect to channel (WebSocket)');
  console.log('2. Send message (REST API)');
  console.log('3. Get messages from channel');
  console.log('4. Get client counts');
  console.log('5. Health check');
  console.log('6. Exit');
  console.log('');
}

async function setupUser() {
  console.log('\n--- User Setup ---');
  console.log('Note: You can send messages to any channel.');
  console.log('To READ messages or SUBSCRIBE to a channel, your user ID or groups must match the channel ID.');
  console.log('Example: User ID "alice" with groups ["2", "3"] can read/subscribe to channels: alice, 2, 3\n');
  
  const userId = await question('Enter your user ID: ');
  const groupsInput = await question('Enter your groups (comma-separated, or press Enter for none): ');
  const groups = groupsInput ? groupsInput.split(',').map(g => g.trim()) : [];
  
  const token = createToken(userId, groups);
  console.log(`\n✓ JWT Token generated for user: ${userId}`);
  if (groups.length > 0) {
    console.log(`  Groups: ${groups.join(', ')}`);
  }
  console.log(`  Can read/subscribe to channels: ${userId}${groups.length > 0 ? ', ' + groups.join(', ') : ''}`);
  
  return { userId, groups, token };
}

async function connectToChannel(token, userId, groups) {
  const channelId = await question('\nEnter channel ID to connect: ');
  
  const wsClient = new ChatWebSocketClient(SERVER_WS, token, channelId);
  
  wsClient.onMessage((message) => {
    console.log(`\n[${new Date(message.created_at).toLocaleTimeString()}] ${message.sender}: ${message.content}`);
  });
  
  try {
    await wsClient.connect();
    console.log('\nListening for messages... (Press Ctrl+C to disconnect)');
    
    process.on('SIGINT', () => {
      console.log('\n\nDisconnecting...');
      wsClient.disconnect();
      process.exit(0);
    });
    
    await new Promise(() => {});
  } catch (error) {
    if (error.message.includes('403') || error.message.includes('forbidden')) {
      console.error(`Failed to connect: forbidden`);
      console.error(`  Your user ID (${userId}) or groups [${groups.join(', ')}] don't match channel "${channelId}"`);
      console.error(`  Tip: Add "${channelId}" to your groups during setup to access this channel`);
    } else {
      console.error(`Failed to connect: ${error.message}`);
    }
  }
}

async function sendMessage(token, userId, groups) {
  const restClient = new ChatRestClient(SERVER_HTTP, token);
  
  const channelId = await question('\nEnter channel ID: ');
  const content = await question('Enter message: ');
  
  try {
    await restClient.sendMessage(channelId, content);
    console.log('✓ Message sent successfully');
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function getMessages(token) {
  const restClient = new ChatRestClient(SERVER_HTTP, token);
  
  const channelId = await question('\nEnter channel ID: ');
  
  try {
    const messages = await restClient.getMessages(channelId);
    console.log(`\n✓ Retrieved ${messages.length} messages:\n`);
    messages.forEach((msg, idx) => {
      console.log(`${idx + 1}. [${new Date(msg.created_at).toLocaleString()}] ${msg.sender}: ${msg.content}`);
    });
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function getClientCounts() {
  const restClient = new ChatRestClient(SERVER_HTTP, '');
  
  const channelsInput = await question('\nEnter channel IDs (comma-separated): ');
  const channels = channelsInput.split(',').map(c => c.trim());
  
  try {
    const counts = await restClient.getClientCounts(channels);
    console.log('\n✓ Client counts:');
    Object.entries(counts).forEach(([channel, count]) => {
      console.log(`  ${channel}: ${count} client(s)`);
    });
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function healthCheck() {
  const restClient = new ChatRestClient(SERVER_HTTP, '');
  
  try {
    const health = await restClient.health();
    console.log('\n✓ Server is healthy');
    console.log(`  Status: ${health.status}`);
    console.log(`  Time: ${health.time}`);
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function main() {
  displayBanner();
  
  const { userId, groups, token } = await setupUser();
  
  let running = true;
  
  while (running) {
    displayMenu();
    const choice = await question('Select an option: ');
    
    switch (choice) {
      case '1':
        await connectToChannel(token, userId, groups);
        break;
      case '2':
        await sendMessage(token, userId, groups);
        break;
      case '3':
        await getMessages(token);
        break;
      case '4':
        await getClientCounts();
        break;
      case '5':
        await healthCheck();
        break;
      case '6':
        console.log('\nGoodbye!\n');
        running = false;
        break;
      default:
        console.log('Invalid option. Please try again.');
    }
  }
  
  rl.close();
}

main().catch(error => {
  console.error('Error:', error);
  process.exit(1);
});
