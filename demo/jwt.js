import jwt from 'jsonwebtoken';

const JWT_SECRET = 'your-jwt-secret';

export function createToken(userId, groups = []) {
  const payload = {
    id: userId,
    groups: groups,
    iat: Math.floor(Date.now() / 1000),
    exp: Math.floor(Date.now() / 1000) + (60 * 60 * 24)
  };
  
  return jwt.sign(payload, JWT_SECRET);
}

export function decodeToken(token) {
  try {
    return jwt.decode(token);
  } catch (error) {
    console.error('Failed to decode token:', error.message);
    return null;
  }
}
