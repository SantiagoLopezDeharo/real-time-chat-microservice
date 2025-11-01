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
  console.log('1. Connect to WebSocket (receive all messages)');
  console.log('2. Send message to a channel');
  console.log('3. Get messages from a channel');
  console.log('4. Get user connection counts');
  console.log('5. Health check');
  console.log('6. Exit');
  console.log('');
}

async function setupUser() {
  console.log('\n--- User Setup ---');
  console.log('Note: Channels are identified by participant user IDs.');
  console.log('Example: A channel between "alice" and "bob" = ["alice", "bob"]');
  console.log('Group chat: ["alice", "bob", "charlie"]\n');
  
  const userId = await question('Enter your user ID: ');
  
  const token = createToken(userId);
  console.log(`\n✓ JWT Token generated for user: ${userId}`);
  
  return { userId, token };
}

async function connectToWebSocket(token, userId) {
  const wsClient = new ChatWebSocketClient(SERVER_WS, token);
  
  wsClient.onMessage((message) => {
    const participants = message.participants.join(', ');
    console.log(`\n[${new Date(message.created_at).toLocaleTimeString()}] Channel [${participants}]`);
    console.log(`  ${message.sender}: ${message.content}`);
  });
  
  try {
    await wsClient.connect();
    console.log(`\n✓ WebSocket connected for user: ${userId}`);
    console.log('Listening for all messages... (Press Ctrl+C to disconnect)');
    
    process.on('SIGINT', () => {
      console.log('\n\nDisconnecting...');
      wsClient.disconnect();
      process.exit(0);
    });
    
    await new Promise(() => {});
  } catch (error) {
    console.error(`Failed to connect: ${error.message}`);
  }
}

async function sendMessage(token, userId) {
  const restClient = new ChatRestClient(SERVER_HTTP, token);
  
  const participantsInput = await question('\nEnter participant user IDs (comma-separated, including yourself): ');
  const participants = participantsInput.split(',').map(p => p.trim());
  
  // Verify user is in participants
  if (!participants.includes(userId)) {
    console.error(`✗ You must be a participant in the channel. Add "${userId}" to the list.`);
    return;
  }
  
  const content = await question('Enter message: ');
  
  try {
    await restClient.sendMessage(participants, content);
    console.log('✓ Message sent successfully');
    console.log(`  Channel: [${participants.join(', ')}]`);
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function getMessages(token) {
  const restClient = new ChatRestClient(SERVER_HTTP, token);
  
  const participantsInput = await question('\nEnter participant user IDs (comma-separated): ');
  const participants = participantsInput.split(',').map(p => p.trim());
  
  const pageInput = await question('Enter page number (default 0): ');
  const page = pageInput ? parseInt(pageInput, 10) : 0;
  
  const sizeInput = await question('Enter page size (default 50, max 100): ');
  const size = sizeInput ? parseInt(sizeInput, 10) : 50;
  
  try {
    const messages = await restClient.getMessages(participants, page, size);
    console.log(`\n✓ Retrieved ${messages.length} messages from channel [${participants.join(', ')}]`);
    console.log(`  (Page ${page}, Size ${size}, Offset ${page * size})\n`);
    messages.forEach((msg, idx) => {
      console.log(`${idx + 1}. [${new Date(msg.created_at).toLocaleString()}] ${msg.sender}: ${msg.content}`);
    });
    
    if (messages.length === 0) {
      console.log('  No messages found on this page.');
    } else if (messages.length < size) {
      console.log(`\n  (Last page - only ${messages.length} messages)`);
    }
  } catch (error) {
    console.error(`✗ ${error.message}`);
  }
}

async function getUserConnections() {
  const restClient = new ChatRestClient(SERVER_HTTP, '');
  
  const usersInput = await question('\nEnter user IDs (comma-separated): ');
  const users = usersInput.split(',').map(u => u.trim());
  
  try {
    const counts = await restClient.getUserConnections(users);
    console.log('\n✓ User connection counts:');
    Object.entries(counts).forEach(([user, count]) => {
      console.log(`  ${user}: ${count} connection(s)`);
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
  
  const { userId, token } = await setupUser();
  
  let running = true;
  
  while (running) {
    displayMenu();
    const choice = await question('Select an option: ');
    
    switch (choice) {
      case '1':
        await connectToWebSocket(token, userId);
        break;
      case '2':
        await sendMessage(token, userId);
        break;
      case '3':
        await getMessages(token);
        break;
      case '4':
        await getUserConnections();
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
