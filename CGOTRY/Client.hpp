#ifndef Client_hpp
#define Client_hpp

#include <stdio.h>
#include<map>
#include<iostream>
#include<v8.h>
#include "v8Instance.hpp"
#include <libplatform/libplatform.h>
#include "queue.h"
#include "Messages.h"

class Engine{
    v8::Platform* platform;
public:
    ~Engine();
    void Compile(std::string msg,const char* code);
    Engine(int NumberOfIsolates);
    void* Route(struct metaData metadoc,const char* doc,std::string filename);
private:
    int NumberOfIsolates;
    v8Instance* workers[64];//Array of isolates
    std::atomic<int> isolateNumber ={0};
    std::atomic<int> Current ={0};
};

#endif /* Client_hpp */

