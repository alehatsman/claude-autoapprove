#!/bin/bash
echo "Running safe commands..."
echo "ls -la"
sleep 1
echo "Next command will be dangerous:"
echo "rm -rf /"
sleep 2
echo "Do you want to continue? [y/n]"
read -t 5 response
echo "Response: '$response'"
