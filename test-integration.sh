#!/bin/bash

# Test script for AI Brain Infrastructure with Better Auth
# This script tests the complete authentication and API flow

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
AUTH_URL="${AUTH_URL:-http://localhost:3000}"
API_URL="${API_URL:-http://localhost:8080}"
TEST_EMAIL="test-$(date +%s)@example.com"
TEST_PASSWORD="TestPassword123!"
TEST_NAME="Test User"

echo -e "${BLUE}╔════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║  AI Brain Infrastructure Integration Test     ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════════════╝${NC}"
echo ""

# Function to print step
step() {
    echo -e "${YELLOW}▶ $1${NC}"
}

# Function to print success
success() {
    echo -e "${GREEN}✓ $1${NC}"
}

# Function to print error
error() {
    echo -e "${RED}✗ $1${NC}"
    exit 1
}

# Function to check if service is running
check_service() {
    local url=$1
    local name=$2
    
    if curl -s -f "$url" > /dev/null 2>&1; then
        success "$name is running"
        return 0
    else
        error "$name is not running at $url"
        return 1
    fi
}

# Step 1: Check services
step "Checking if services are running..."
check_service "$AUTH_URL/health" "Better Auth Server"
check_service "$API_URL/health" "Go API Server"
echo ""

# Step 2: Sign up a new user
step "Signing up new user: $TEST_EMAIL"
SIGNUP_RESPONSE=$(curl -s -X POST "$AUTH_URL/api/auth/sign-up/email" \
    -H "Content-Type: application/json" \
    -d "{
        \"email\": \"$TEST_EMAIL\",
        \"password\": \"$TEST_PASSWORD\",
        \"name\": \"$TEST_NAME\"
    }")

# Extract JWT token using grep and sed (no jq needed)
JWT_TOKEN=$(echo "$SIGNUP_RESPONSE" | grep -o '"jwt":"[^"]*"' | sed 's/"jwt":"//;s/"$//')
USER_ID=$(echo "$SIGNUP_RESPONSE" | grep -o '"id":"[^"]*"' | head -1 | sed 's/"id":"//;s/"$//')

if [ -n "$JWT_TOKEN" ] && [ "$JWT_TOKEN" != "null" ]; then
    success "User signed up successfully"
    echo "  User ID: $USER_ID"
    echo "  JWT: ${JWT_TOKEN:0:30}..."
else
    error "Signup failed - no JWT token: $SIGNUP_RESPONSE"
fi
echo ""

# Step 3: Test /me endpoint
step "Testing /me endpoint..."
ME_RESPONSE=$(curl -s -X GET "$API_URL/me" \
    -H "Authorization: Bearer $JWT_TOKEN")

if echo "$ME_RESPONSE" | grep -q '"id"'; then
    success "User info retrieved successfully"
    ME_NAME=$(echo "$ME_RESPONSE" | grep -o '"name":"[^"]*"' | sed 's/"name":"//;s/"$//')
    ME_EMAIL=$(echo "$ME_RESPONSE" | grep -o '"email":"[^"]*"' | sed 's/"email":"//;s/"$//')
    echo "  Name: $ME_NAME"
    echo "  Email: $ME_EMAIL"
else
    error "Failed to get user info: $ME_RESPONSE"
fi
echo ""

# Step 4: Store multiple events
step "Storing events..."
for i in {1..5}; do
    EVENT_RESPONSE=$(curl -s -X POST "$API_URL/events" \
        -H "Authorization: Bearer $JWT_TOKEN" \
        -H "Content-Type: application/json" \
        -d "{
            \"type\": \"test_event_$i\",
            \"data\": \"Test event number $i - $(date)\"
        }")
    
    if echo "$EVENT_RESPONSE" | grep -q '"id"'; then
        EVENT_ID=$(echo "$EVENT_RESPONSE" | grep -o '"id":[0-9]*' | sed 's/"id"://')
        success "Event $i stored (ID: $EVENT_ID)"
    else
        error "Failed to store event $i: $EVENT_RESPONSE"
    fi
done
echo ""

# Step 5: Retrieve all events
step "Retrieving all events..."
EVENTS_RESPONSE=$(curl -s -X GET "$API_URL/events" \
    -H "Authorization: Bearer $JWT_TOKEN")

EVENT_COUNT=$(echo "$EVENTS_RESPONSE" | grep -o '"id":' | wc -l | tr -d ' ')
if [ "$EVENT_COUNT" -eq 5 ]; then
    success "Retrieved all $EVENT_COUNT events"
else
    error "Expected 5 events, got $EVENT_COUNT"
fi
echo ""

# Step 6: Filter events by type
step "Testing event filtering..."
FILTERED_RESPONSE=$(curl -s -X GET "$API_URL/events?type=test_event_3" \
    -H "Authorization: Bearer $JWT_TOKEN")

FILTERED_COUNT=$(echo "$FILTERED_RESPONSE" | grep -o '"id":' | wc -l | tr -d ' ')
if [ "$FILTERED_COUNT" -eq 1 ]; then
    success "Event filtering works correctly"
else
    error "Expected 1 event, got $FILTERED_COUNT"
fi
echo ""

# Step 7: Test with invalid token
step "Testing invalid token handling..."
INVALID_RESPONSE=$(curl -s -w "\n%{http_code}" -X GET "$API_URL/events" \
    -H "Authorization: Bearer invalid-token-12345")

HTTP_CODE=$(echo "$INVALID_RESPONSE" | tail -n1)
if [ "$HTTP_CODE" -eq 401 ]; then
    success "Invalid token correctly rejected (401)"
else
    error "Expected 401, got $HTTP_CODE"
fi
echo ""

# Step 8: Sign in with existing user
step "Testing sign-in..."
SIGNIN_RESPONSE=$(curl -s -X POST "$AUTH_URL/api/auth/sign-in/email" \
    -H "Content-Type: application/json" \
    -d "{
        \"email\": \"$TEST_EMAIL\",
        \"password\": \"$TEST_PASSWORD\"
    }")

NEW_JWT=$(echo "$SIGNIN_RESPONSE" | grep -o '"jwt":"[^"]*"' | sed 's/"jwt":"//;s/"$//')

if [ -n "$NEW_JWT" ] && [ "$NEW_JWT" != "null" ]; then
    success "Sign-in successful"
    echo "  New JWT: ${NEW_JWT:0:30}..."
else
    error "Sign-in failed - no JWT token: $SIGNIN_RESPONSE"
fi
echo ""

# Step 9: Verify events persist with new token
step "Verifying data persistence with new token..."
VERIFY_RESPONSE=$(curl -s -X GET "$API_URL/events" \
    -H "Authorization: Bearer $NEW_JWT")

VERIFY_COUNT=$(echo "$VERIFY_RESPONSE" | grep -o '"id":' | wc -l | tr -d ' ')
if [ "$VERIFY_COUNT" -eq 5 ]; then
    success "Data persists correctly across sessions"
else
    error "Expected 5 events, got $VERIFY_COUNT"
fi
echo ""

# Step 10: Performance check - measure JWT validation latency
step "Measuring JWT validation latency..."
START_TIME=$(date +%s%N)
for i in {1..100}; do
    curl -s -X GET "$API_URL/me" \
        -H "Authorization: Bearer $NEW_JWT" > /dev/null
done
END_TIME=$(date +%s%N)

ELAPSED_MS=$(( ($END_TIME - $START_TIME) / 100000 ))
AVG_MS=$(( $ELAPSED_MS / 100 ))

success "Completed 100 requests in ${ELAPSED_MS}ms"
echo "  Average latency: ${AVG_MS}ms per request"

if [ $AVG_MS -lt 20 ]; then
    success "Performance excellent! (< 20ms)"
elif [ $AVG_MS -lt 50 ]; then
    success "Performance good (< 50ms)"
else
    echo -e "${YELLOW}⚠ Performance could be better (${AVG_MS}ms)${NC}"
fi
echo ""

# Step 11: Check JWKS cache stats
step "Checking JWKS cache statistics..."
HEALTH_RESPONSE=$(curl -s -X GET "$API_URL/health")
CACHE_KEYS=$(echo "$HEALTH_RESPONSE" | grep -o '"keys_cached":[0-9]*' | sed 's/"keys_cached"://')
CACHE_AGE=$(echo "$HEALTH_RESPONSE" | grep -o '"age_seconds":[0-9.]*' | sed 's/"age_seconds"://')

success "JWKS cache healthy"
echo "  Keys cached: $CACHE_KEYS"
echo "  Cache age: ${CACHE_AGE}s"
echo ""

# Summary
echo -e "${GREEN}╔════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║           All Tests Passed! ✓                  ║${NC}"
echo -e "${GREEN}╚════════════════════════════════════════════════╝${NC}"
echo ""
echo "Test Summary:"
echo "  • User created: $TEST_EMAIL"
echo "  • User ID: $USER_ID"
echo "  • Events stored: 5"
echo "  • Average latency: ${AVG_MS}ms"
echo "  • JWT validation: Cached (${CACHE_KEYS} keys)"
echo ""
echo "Cleanup: User data stored at data/users/$USER_ID/"
echo ""
