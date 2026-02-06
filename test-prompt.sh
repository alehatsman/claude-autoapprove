#!/bin/bash
echo "Starting test..."
sleep 1
echo "Do you want to continue? [y/n]"
read -t 5 response
echo "Response received: '$response'"
if [ "$response" = "y" ]; then
    echo "✓ Auto-approve worked!"
else
    echo "✗ No response or wrong response"
fi
